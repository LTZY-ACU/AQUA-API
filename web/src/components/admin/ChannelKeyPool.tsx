/** 渠道密钥池运行态：每把密钥的存活状况 + 冷却倒计时 + 订阅账号额度窗口。
 *
 * 意图（Why）：
 *   密钥池是"多把上游凭据轮询"的调度单元，它出事时的表现是"渠道整体时好时坏"——
 *   只有把每把密钥的【冷却剩余 / 连续失败 / 失败原因】摆出来，站长才能判断
 *   是"个别密钥坏了"还是"整个上游在抖"。冷却中的密钥给 warn 色 + 倒计时，
 *   让"什么时候会恢复"一眼可见。
 *
 *   订阅账号（OAuth 凭据）还多一层信息：上游给了"5 小时"与"每周"两条独立的
 *   额度线。只盯主窗口会在周额度将满时毫无预警（"明明没怎么用，账号却突然全满"），
 *   因此这里对订阅账号同时渲染两条进度条 + 重置倒计时。
 *
 * 流转（Flow）：
 *   渠道编辑弹层 → <ChannelKeyPool channelId={id}/> → listChannelKeysWithBalance(id)
 *   → GET /api/admin/channels/:id/keys
 *   点「查询额度」→ probeChannelKeyQuota(id, keyId)
 *   → POST /api/admin/channels/:id/keys/:keyId/quota → 落库后重新拉列表刷新
 *   行内「启用 / 禁用 / 恢复」→ updateChannelKey(keyId, { status })
 *   → PUT /api/admin/keys/:keyId（admin.ts 封装，只改状态）
 *   「编辑」弹层 → updateChannelKeyBalance(keyId, payload)
 *   → 同一个 PUT /api/admin/keys/:keyId（channel.ts 封装额外允许 balance 字段）。
 *   字段全部来自后端 channelKeyDTO（见 internal/server/dto.go），前端不臆造字段名。
 *
 * 扩展（Extend）：
 *   - 冷却/额度重置倒计时由本地 setInterval 每秒重算剩余时间实现，无需后端推送；
 *   - 需要"RPM 使用率"时后端需补「当前分钟已用请求数」字段（当前 DTO 只有 rpm_limit 上限
 *     与 in_flight 在途数，二者单位不同，不能相除当使用率）；本组件暂以「限速 / 在途」展示；
 *   - 「余额/额度耗尽」判定直接用后端下发的派生布尔 balance_exhausted / quota_exhausted，
 *     规则只在领域层维护一处，前端不再自行实现阈值；
 *   - 额度的"是否已探测"同样用后端派生布尔 quota_known / quota_secondary_known，
 *     未知时显示「未查询」而不是把 -1 画成 0%；
 *   - 写操作的分节规则（与后端 handleUpdateChannelKeyStatus 的校验一一对应）：
 *     weight / priority / rpm_limit 三项必须同时提交（底层整组覆盖，缺项会被写成 0）；
 *     groups / models 两项必须同时提交（缺项会被误清成"不限"）；balance 仅在显式
 *     变化时提交——余额录入会刷新 balance_updated_at，顺手保存别的分节不该动它。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import {
  listChannelKeysWithBalance,
  probeChannelKeyQuota,
  updateChannelKeyBalance,
  type ChannelKeyWithBalance,
  type UpdateChannelKeyBalancePayload,
} from '@/api/channel'
import { updateChannelKey } from '@/api/admin'
import {
  KEY_STATUS_AUTO_REMOVED,
  KEY_STATUS_DISABLED,
  KEY_STATUS_ENABLED,
  type ChannelKey,
} from '@/api/types'
import { Badge, EmptyState, SkeletonRows } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Form'
import { ConfirmDialog, Modal } from '@/components/ui/Modal'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime, joinModelList, parseModelList } from '@/utils/format'

interface ChannelKeyPoolProps {
  /** 渠道 ID；变化时重新拉取该渠道的密钥明细 */
  channelId: number
}

/** 冷却剩余秒数（<=0 表示不在冷却）。cooldown_until 是 Unix 秒，now 是毫秒时间戳 */
function cooldownSeconds(cooldownUntil: number, nowMs: number): number {
  if (!cooldownUntil) return 0
  return Math.max(0, cooldownUntil - Math.floor(nowMs / 1000))
}

/** 剩余秒 → 「还有 X 分 Y 秒 / X 时 Y 分」 */
function formatCooldown(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  if (h > 0) return `还有 ${h} 时 ${m} 分`
  if (m > 0) return `还有 ${m} 分 ${s} 秒`
  return `还有 ${s} 秒`
}

/**
 * 窗口秒数 → 中文窗口名。
 *
 * 上游给出窗口时长时按它命名（如 18000 秒 = "5 小时窗口"），
 * 未提供（0）时退回调用方给的默认文案，避免显示"0 秒窗口"这种废话。
 */
function windowLabel(seconds: number, fallback: string): string {
  if (!seconds || seconds <= 0) return fallback
  const week = 7 * 24 * 3600
  if (seconds % week === 0) return `${seconds / week} 周窗口`
  if (seconds % 86400 === 0) return `${seconds / 86400} 天窗口`
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时窗口`
  return `${Math.max(1, Math.round(seconds / 60))} 分钟窗口`
}

/** 判断两个字符串清单是否逐项相等（路由分叉"是否被改动"的判定，顺序敏感） */
function sameStringList(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  return a.every((value, index) => value === b[index])
}

/** 密钥状态 → 徽标（文案优先用后端下发的 status_text，避免前端硬编码中英映射） */
function KeyStatusBadge({ item }: { item: ChannelKey }) {
  if (item.status === KEY_STATUS_ENABLED) return <Badge tone="ok">{item.status_text || '启用'}</Badge>
  if (item.status === KEY_STATUS_AUTO_REMOVED) return <Badge tone="err">{item.status_text || '已摘除'}</Badge>
  return <Badge tone="off">{item.status_text || '禁用'}</Badge>
}

/**
 * 单条额度窗口进度条（主窗口 / 次窗口各渲染一条）。
 *
 * known 为 false 时显示「未查询」且不画填充：把"没查过"画成 0% 会让站长
 * 误以为"额度充足"，这是本任务明确要避免的语义错误。
 * 颜色沿用站内语义色（ok / warn / err），三套主题下自动适配。
 */
function QuotaWindowBar({
  label,
  usedPercent,
  known,
  resetAt,
  nowMs,
}: {
  label: string
  usedPercent: number
  known: boolean
  resetAt: number
  nowMs: number
}) {
  const percent = known ? Math.min(100, Math.max(0, usedPercent)) : 0
  const tone = !known ? '' : percent >= 100 ? 'bg-err' : percent >= 80 ? 'bg-warn' : 'bg-ok'
  const remaining = resetAt > 0 ? Math.max(0, resetAt - Math.floor(nowMs / 1000)) : 0

  return (
    <div className="min-w-[140px]">
      <div className="flex items-center justify-between gap-2 text-xs">
        <span className="text-ink-3">{label}</span>
        <span className="tabular-nums text-ink-2">{known ? `${percent}%` : '未查询'}</span>
      </div>
      <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-ink/10">
        {known && <div className={`h-full rounded-full ${tone}`} style={{ width: `${percent}%` }} />}
      </div>
      {known && remaining > 0 && (
        <div className="mt-0.5 text-[11px] text-ink-3">{formatCooldown(remaining).replace('还有 ', '')}后重置</div>
      )}
    </div>
  )
}

export function ChannelKeyPool({ channelId }: ChannelKeyPoolProps) {
  const [items, setItems] = useState<ChannelKeyWithBalance[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  // 每秒刷新的"当前时刻"，用于把 cooldown_until / 额度重置时间换算成倒计时并在到期后自动变回可用
  const [now, setNow] = useState(() => Date.now())
  // 正在查询额度的凭据 ID（0 表示空闲）；同时用于禁用该行按钮，避免重复触发上游请求
  const [probingId, setProbingId] = useState(0)
  const [probeError, setProbeError] = useState<string | null>(null)
  // 写操作状态：
  //   actingId —— 正在切换状态的密钥 ID（0 = 空闲），驱动行内 loading 与防重复点击；
  //   disabling —— 待二次确认禁用的密钥（禁用会让凭据立即退出调度，属危险操作）；
  //   editing —— 正在编辑调度参数 / 余额 / 路由分叉的密钥。
  const [actingId, setActingId] = useState(0)
  const [disabling, setDisabling] = useState<ChannelKeyWithBalance | null>(null)
  const [editing, setEditing] = useState<ChannelKeyWithBalance | null>(null)
  const { toast, toastError } = useToast()

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await listChannelKeysWithBalance(channelId)
      setItems(data.items)
    } catch (err) {
      setError(err instanceof Error ? err.message : '密钥池加载失败')
    } finally {
      setLoading(false)
    }
  }, [channelId])

  useEffect(() => {
    void load()
  }, [load])

  // 倒计时心跳：组件卸载（弹层关闭）时必须清理，否则会持续 setState 造成泄漏
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])

  // 查询某把订阅账号的额度：探测成功后重新拉列表（后端已把结果落库，重拉即可拿到最新快照）
  const handleProbe = useCallback(
    async (keyId: number) => {
      setProbingId(keyId)
      setProbeError(null)
      try {
        await probeChannelKeyQuota(channelId, keyId)
        await load()
      } catch (err) {
        setProbeError(err instanceof Error ? err.message : '额度查询失败')
      } finally {
        setProbingId(0)
      }
    },
    [channelId, load],
  )

  // 修改单把密钥的状态（启用 / 禁用 / 恢复共用同一个 PUT 接口，只提交 status 一项）。
  //
  // 「恢复」针对被连续失败自动摘除（status=3）或手动禁用的密钥：状态回到启用即重新
  // 参与调度；若上游真坏了它会再次被自动摘除，因此无需额外的失败计数重置语义。
  // 冷却（cooldown_until）是运行态字段、到期自动结束，该接口不涉及也无法干预。
  // 返回是否成功，调用方据此决定是否收起二次确认弹层（失败保留弹层便于重试）。
  const handleSetStatus = useCallback(
    async (item: ChannelKeyWithBalance, next: number): Promise<boolean> => {
      setActingId(item.id)
      try {
        await updateChannelKey(item.id, { status: next })
        toast(next === KEY_STATUS_ENABLED ? '密钥已启用' : '密钥已禁用')
        await load()
        return true
      } catch (err) {
        toastError(err instanceof Error ? err.message : '状态更新失败')
        return false
      } finally {
        setActingId(0)
      }
    },
    [load, toast, toastError],
  )

  // 二次确认后执行禁用；成功才关弹层
  const handleConfirmDisable = useCallback(async () => {
    if (!disabling) return
    if (await handleSetStatus(disabling, KEY_STATUS_DISABLED)) setDisabling(null)
  }, [disabling, handleSetStatus])

  const coolingCount = items?.filter((k) => cooldownSeconds(k.cooldown_until, now) > 0).length ?? 0
  // 被「余额/额度耗尽」排除的凭据数：余额（API Key）与订阅额度（OAuth）任一耗尽即计入，
  // 判定直接用后端派生布尔，避免前端另行实现阈值规则。
  const exhaustedCount = items?.filter((k) => k.balance_exhausted || k.quota_exhausted).length ?? 0
  // 整池都耗尽：调度会跳过全部凭据，渠道事实上不可用——必须让站长一眼看到并去补录。
  const allExhausted = (items?.length ?? 0) > 0 && exhaustedCount === items?.length

  const header = (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div className="flex items-center gap-2">
        <h3 className="text-[13px] font-semibold text-ink">密钥池运行态</h3>
        {items && items.length > 0 && (
          <span className="text-xs text-ink-3">
            共 {items.length} 把{coolingCount > 0 ? ` · ${coolingCount} 把冷却中` : ''}
          </span>
        )}
      </div>
      {allExhausted ? (
        <Badge tone="err">全部凭据余额/额度耗尽</Badge>
      ) : exhaustedCount > 0 ? (
        <Badge tone="warn">{exhaustedCount} 把余额/额度耗尽</Badge>
      ) : coolingCount > 0 ? (
        <Badge tone="warn">{coolingCount} 把冷却中</Badge>
      ) : null}
    </div>
  )

  if (loading && items === null) {
    return (
      <div className="space-y-3">
        {header}
        <SkeletonRows rows={2} />
      </div>
    )
  }

  if (error) {
    return (
      <div className="space-y-3">
        {header}
        <div className="flex items-center gap-3 text-[13px] text-err">
          <span>{error}</span>
          <Button variant="secondary" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      </div>
    )
  }

  if (!items || items.length === 0) {
    return (
      <div className="space-y-3">
        {header}
        <div className="rounded-md border border-line">
          <EmptyState title="该渠道未配置密钥池" description="仍走单密钥模式；需要多把凭据轮询时可在下方表单批量粘贴。" />
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-3">
      {header}
      {probeError && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-err/40 bg-err/10 px-3 py-2 text-[13px] text-err">
          <Badge tone="err">额度查询失败</Badge>
          <span>{probeError}</span>
        </div>
      )}
      {allExhausted && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-err/40 bg-err/10 px-3 py-2 text-[13px] text-err">
          <Badge tone="err">余额耗尽</Badge>
          <span>该渠道全部 {items?.length} 把凭据的余额/额度已耗尽，调度会跳过整池。请补录余额或更换凭据。</span>
        </div>
      )}
      <div className="overflow-hidden rounded-md border border-line">
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-[13px]">
            <thead>
              <tr className="border-b border-line bg-surface/70 text-ink-3">
                <th className="px-3 py-2 text-left font-medium">密钥</th>
                <th className="px-3 py-2 text-left font-medium">额度窗口</th>
                <th className="px-3 py-2 text-left font-medium">状态</th>
                <th className="px-3 py-2 text-left font-medium">冷却</th>
                <th className="px-3 py-2 text-right font-medium">连续失败</th>
                <th className="hidden px-3 py-2 text-left font-medium sm:table-cell">失败原因</th>
                <th className="hidden px-3 py-2 text-right font-medium md:table-cell">限速 / 在途</th>
                <th className="px-3 py-2 text-right font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => {
                const remaining = cooldownSeconds(item.cooldown_until, now)
                const cooling = remaining > 0
                return (
                  <tr
                    key={item.id}
                    className={`border-b border-line/70 last:border-0 ${cooling ? 'bg-warn/5' : ''}`}
                  >
                    {/* 掩码密钥 + 备注；kind_text 让"订阅账号"与"API Key"一眼可分 */}
                    <td className="px-3 py-2 align-top">
                      <div className="font-mono text-ink-2">{item.masked_key || '—'}</div>
                      <div className="mt-0.5 flex items-center gap-1.5 text-xs text-ink-3">
                        <span>{item.kind_text || item.kind}</span>
                        {item.email && <span className="truncate" title={item.email}>· {item.email}</span>}
                        {!item.email && item.label && <span className="truncate" title={item.label}>· {item.label}</span>}
                      </div>
                    </td>
                    {/* 订阅账号展示 5 小时 / 每周两条额度进度条；API Key 无额度窗口 */}
                    <td className="px-3 py-2 align-top">
                      {item.kind === 'oauth' ? (
                        <div className="space-y-2">
                          <QuotaWindowBar
                            label={windowLabel(item.quota_primary_window_seconds, '5 小时窗口')}
                            usedPercent={item.quota_used_percent}
                            known={item.quota_known}
                            resetAt={item.quota_reset_at}
                            nowMs={now}
                          />
                          <QuotaWindowBar
                            label={windowLabel(item.quota_secondary_window_seconds, '每周窗口')}
                            usedPercent={item.quota_secondary_used_percent}
                            known={item.quota_secondary_known}
                            resetAt={item.quota_secondary_reset_at}
                            nowMs={now}
                          />
                          <div className="flex flex-wrap items-center gap-2">
                            <Button
                              variant="secondary"
                              size="sm"
                              disabled={probingId === item.id}
                              onClick={() => void handleProbe(item.id)}
                            >
                              {probingId === item.id ? '查询中…' : '查询额度'}
                            </Button>
                            {item.plan_type && <span className="text-xs text-ink-3">{item.plan_type}</span>}
                          </div>
                        </div>
                      ) : (
                        <span className="text-ink-3">—</span>
                      )}
                    </td>
                    <td className="px-3 py-2 align-top">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <KeyStatusBadge item={item} />
                        {(item.balance_exhausted || item.quota_exhausted) && (
                          <Badge tone="err">{item.balance_exhausted ? '余额耗尽' : '额度耗尽'}</Badge>
                        )}
                      </div>
                    </td>
                    <td className="px-3 py-2 align-top">
                      {cooling ? (
                        <Badge tone="warn">{formatCooldown(remaining)}</Badge>
                      ) : (
                        <span className="text-ink-3">—</span>
                      )}
                    </td>
                    <td className="px-3 py-2 text-right align-top tabular-nums">
                      {item.fail_count > 0 ? <span className="text-warn">{item.fail_count}</span> : <span className="text-ink-3">0</span>}
                    </td>
                    <td className="hidden max-w-56 px-3 py-2 align-top sm:table-cell">
                      {item.last_error ? (
                        <span className="block truncate text-ink-3" title={item.last_error}>
                          {item.last_error}
                        </span>
                      ) : (
                        <span className="text-ink-3">—</span>
                      )}
                    </td>
                    <td className="hidden px-3 py-2 text-right align-top tabular-nums md:table-cell">
                      <span className="text-ink-2">{item.rpm_limit > 0 ? `限速 ${item.rpm_limit}` : '不限速'}</span>
                      {item.in_flight > 0 && <span className="text-ink-3"> · 在途 {item.in_flight}</span>}
                    </td>
                    {/* 操作列：启用/禁用切换 + 恢复 + 编辑。
                        禁用会让凭据立即退出调度，先走二次确认（确认弹层本身用 danger 红色
                        承载"这是危险操作"的语义，行内按钮保持中性外观即可）；
                        启用/恢复是低风险操作直接执行。actingId 驱动本行按钮的 loading/禁用，
                        防止重复点击造成并发状态变更。 */}
                    <td className="px-3 py-2 align-top">
                      <div className="flex flex-wrap items-center justify-end gap-1.5">
                        {item.status === KEY_STATUS_ENABLED ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={actingId === item.id}
                            onClick={() => setDisabling(item)}
                          >
                            禁用
                          </Button>
                        ) : (
                          <Button
                            variant="ghost"
                            size="sm"
                            loading={actingId === item.id}
                            onClick={() => void handleSetStatus(item, KEY_STATUS_ENABLED)}
                          >
                            {item.status === KEY_STATUS_AUTO_REMOVED ? '恢复' : '启用'}
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={actingId === item.id}
                          onClick={() => setEditing(item)}
                        >
                          编辑
                        </Button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>
      {/* 禁用二次确认：误禁用会削减整池可用凭据，高峰期可能直接影响渠道可用性。 */}
      <ConfirmDialog
        open={Boolean(disabling)}
        title="禁用密钥"
        message={
          disabling
            ? `确认禁用 ${disabling.masked_key || '该密钥'}？它将立即退出调度，直到手动启用才会恢复。`
            : undefined
        }
        danger
        confirmText="禁用"
        loading={disabling !== null && actingId === disabling.id}
        onConfirm={() => void handleConfirmDisable()}
        onCancel={() => setDisabling(null)}
      />
      {/* 编辑弹层：调度参数 / 余额快照 / 分组模型分叉 */}
      <KeyEditModal
        item={editing}
        onClose={() => setEditing(null)}
        onSaved={() => {
          setEditing(null)
          void load()
        }}
      />
    </div>
  )
}

/* ── 密钥编辑弹层：调度参数 + 余额快照 + 分组/模型路由分叉 ── */

/**
 * 编辑单把密钥的可写参数。
 *
 * 为什么按「分节」而不是全量提交：后端 PUT /api/admin/keys/:keyId 是部分更新语义，
 *   - weight / priority / rpm_limit 三项必须同时给（底层整组覆盖，缺项会被写成 0）；
 *   - groups / models 两项必须同时给（缺项会被误清成"不限"，凭据会重新参与所有请求）；
 *   - balance 只在显式变化时提交——录入余额会刷新 balance_updated_at，
 *     顺手保存别的分节不该悄悄改掉"上次录入时间"。
 * 因此这里逐节比对原值，只把有变化的分节放进请求体；三节都没动则不发请求。
 */
function KeyEditModal({
  item,
  onClose,
  onSaved,
}: {
  /** 正在编辑的密钥；null 表示弹层关闭 */
  item: ChannelKeyWithBalance | null
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const [weight, setWeight] = useState('0')
  const [priority, setPriority] = useState('0')
  const [rpmLimit, setRpmLimit] = useState('0')
  const [balance, setBalance] = useState('')
  const [groups, setGroups] = useState('')
  const [models, setModels] = useState('')
  const [saving, setSaving] = useState(false)

  // 每次打开（item 从 null 变为某行数据）时用该行现值回填；
  // 关闭时不重置，避免下一次打开瞬间闪现旧值
  useEffect(() => {
    if (!item) return
    setWeight(String(item.weight))
    setPriority(String(item.priority))
    setRpmLimit(String(item.rpm_limit))
    // 余额未录入（-1）时留空，配合帮助文案「留空 = 不修改」
    setBalance(item.balance_unknown ? '' : String(item.balance))
    setGroups(joinModelList(item.groups))
    setModels(joinModelList(item.models))
  }, [item])

  async function handleSubmit() {
    if (!item) return
    // 调度参数校验：后端只拒负数，这里同时拦空值与小数——
    // 空值经 Number() 会静默变成 0（权重 0 = 不参与加权，语义变化太大），
    // 小数在调度语义里没有意义，早失败好过落库后才发现填错。
    if (weight.trim() === '' || priority.trim() === '' || rpmLimit.trim() === '') {
      toastError('请完整填写权重 / 优先级 / RPM 上限')
      return
    }
    const weightValue = Number(weight)
    const priorityValue = Number(priority)
    const rpmValue = Number(rpmLimit)
    if (![weightValue, priorityValue, rpmValue].every((v) => Number.isInteger(v) && v >= 0)) {
      toastError('权重 / 优先级 / RPM 上限必须是 ≥0 的整数')
      return
    }
    // 余额：留空 = 不修改；填写时必须是 ≥-1 的整数（-1 清除录入，0 表示已用尽）
    const balanceRaw = balance.trim()
    let balanceValue: number | null = null
    if (balanceRaw !== '') {
      const parsed = Number(balanceRaw)
      if (!Number.isInteger(parsed) || parsed < -1) {
        toastError('余额必须是 ≥-1 的整数（-1 = 清除录入，0 = 已用尽）')
        return
      }
      balanceValue = parsed
    }

    const payload: UpdateChannelKeyBalancePayload = {}
    // 分节一：调度参数（任一项变化即三项同时提交）
    if (weightValue !== item.weight || priorityValue !== item.priority || rpmValue !== item.rpm_limit) {
      payload.weight = weightValue
      payload.priority = priorityValue
      payload.rpm_limit = rpmValue
    }
    // 分节二：余额（仅在显式填写且与当前值不同时提交，保护 balance_updated_at）
    if (balanceValue !== null && balanceValue !== item.balance) payload.balance = balanceValue
    // 分节三：路由分叉。parseModelList 兼容中英文逗号 / 空格 / 换行分隔（分组与模型共用）；
    // 两项必须同时提交，即使只改了其中一项。
    const groupList = parseModelList(groups)
    const modelList = parseModelList(models)
    if (!sameStringList(groupList, item.groups) || !sameStringList(modelList, item.models)) {
      payload.groups = groupList
      payload.models = modelList
    }

    // 三节都没变化：后端对空请求体直接 400「未提供任何可更新字段」，这里提前拦截
    if (Object.keys(payload).length === 0) {
      toast('没有需要保存的变更')
      onClose()
      return
    }

    setSaving(true)
    try {
      await updateChannelKeyBalance(item.id, payload)
      toast('密钥已更新')
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '密钥更新失败')
    } finally {
      setSaving(false)
    }
  }

  // 余额帮助文案：讲清「这是上游剩余额度快照、耗尽自动退出调度、补录自动恢复」的闭环，
  // 并附上次录入时间，帮助站长判断快照是否过时。
  const balanceHelp = item
    ? `上游剩余额度快照（不参与计费）；已知耗尽会自动退出调度，补录后自动恢复。留空不修改，-1 = 清除录入，0 = 已用尽${
        item.balance_updated_at > 0 ? `。上次录入：${formatDateTime(item.balance_updated_at)}` : ''
      }`
    : ''

  return (
    <Modal open={Boolean(item)} onClose={onClose} title="编辑密钥" width={560}>
      {item && (
        <>
          <div className="space-y-4">
            {/* 凭据标识行：确认"正在改哪一把"——掩码 + 类型 + 账号/备注 */}
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border border-line bg-surface/60 px-3 py-2 text-[13px]">
              <span className="font-mono text-ink-2">{item.masked_key || '—'}</span>
              <span className="text-ink-3">
                {item.kind_text}
                {item.email ? ` · ${item.email}` : item.label ? ` · ${item.label}` : ''}
              </span>
            </div>

            <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <Field label="权重" help="加权随机调度的份额；全 0 退化为等概率">
                <Input value={weight} onChange={(e) => setWeight(e.target.value)} type="number" min={0} step="1" />
              </Field>
              <Field label="优先级" help="顺序调度时越大越先用">
                <Input value={priority} onChange={(e) => setPriority(e.target.value)} type="number" min={0} step="1" />
              </Field>
              <Field label="RPM 上限" help="每分钟请求上限；0 = 不限">
                <Input value={rpmLimit} onChange={(e) => setRpmLimit(e.target.value)} type="number" min={0} step="1" />
              </Field>
            </div>

            <Field label="余额快照" help={balanceHelp}>
              <Input
                value={balance}
                onChange={(e) => setBalance(e.target.value)}
                type="number"
                min={-1}
                step="1"
                placeholder="留空不修改"
              />
            </Field>

            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field label="分组限制" help="逗号分隔；留空 = 不限（继承渠道路由）">
                <Input value={groups} onChange={(e) => setGroups(e.target.value)} placeholder="如 vip, pro" />
              </Field>
              <Field label="模型限制" help="逗号分隔，支持尾部通配符 *；留空 = 不限">
                <Input value={models} onChange={(e) => setModels(e.target.value)} placeholder="如 gpt-4*, claude-*" />
              </Field>
            </div>
          </div>
          <div className="mt-5 flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              取消
            </Button>
            <Button variant="primary" loading={saving} onClick={handleSubmit}>
              保存
            </Button>
          </div>
        </>
      )}
    </Modal>
  )
}
