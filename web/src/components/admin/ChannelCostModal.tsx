/** 渠道 × 模型的上游进价管理弹层。
 *
 * 意图（Why）：
 *   /admin/finance 财务对账页的「成本 / 毛利」列完全来自 channel_model_costs 表——
 *   在此录入之前那两列永远是空的，毛利核算形同虚设。本弹层是该表的唯一录入入口，
 *   并排展示「按密钥用量估算」，让站长录价时立刻看到两件事：
 *   ① 哪些模型已产生用量但还没录进价（漏录 = 成本被按 0 计算，毛利虚高）；
 *   ② 每把密钥按当前进价估算的累计消耗与剩余（余额快照何时该更新）。
 *
 * 流转（Flow）：
 *   渠道列表操作列「进价」按钮 → <ChannelCostModal channel/>
 *   ├─ listChannelCosts / saveChannelCosts → GET/PUT /api/admin/channels/:id/costs
 *   └─ fetchChannelKeyUsage（只读参考）→ GET /api/admin/channels/:id/key-usage
 *   价格以人民币录入（¥/1M token、¥/次），提交时经 yuanToQuota 换算为站内额度，
 *   回显时经 quotaToYuanInput 换算回来——与 /admin/prices 计价页同一套口径，
 *   保证「售价 − 进价」的毛利减法在单位上成立。
 *
 * 扩展（Extend）：
 *   后端新增价格维度（handler_channel_cost.go 的 DTO）时：同步 types.ts 的
 *   ChannelModelCost / ChannelModelCostPayload 与本文件 CostRow 行模型，
 *   并在表格加一列输入框（列头 / 输入框 / 提交字段三处同步）。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { fetchChannelKeyUsage, listChannelCosts, saveChannelCosts } from '@/api/cost'
import { KEY_STATUS_AUTO_REMOVED, KEY_STATUS_ENABLED, type Channel, type ChannelKeyUsage, type ChannelModelCostPayload } from '@/api/types'
import { Badge, SkeletonRows } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Form'
import { Modal } from '@/components/ui/Modal'
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { formatYuanFromQuota, quotaToYuanInput, yuanToQuota } from '@/utils/money'

interface ChannelCostModalProps {
  open: boolean
  channel: Channel | null
  onClose: () => void
}

/** 编辑中的一行进价。价格以人民币字符串保存（输入即所见），提交时才换算成额度。 */
interface CostRow {
  key: number
  model: string
  prompt: string
  cache: string
  completion: string
  perCall: string
  remark: string
}

/** 新行 key 用负数自减：与库中条目的自增 id 空间天然不冲突，增删行时 React key 保持稳定。 */
let nextRowKey = -1

function emptyRow(model = ''): CostRow {
  return { key: nextRowKey--, model, prompt: '', cache: '', completion: '', perCall: '', remark: '' }
}

export function ChannelCostModal({ open, channel, onClose }: ChannelCostModalProps) {
  const { toast, toastError } = useToast()
  const { quotaPerYuan } = useSite()
  const [rows, setRows] = useState<CostRow[]>([])
  const [declaredModels, setDeclaredModels] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [usage, setUsage] = useState<ChannelKeyUsage[] | null>(null)
  const [usageLoading, setUsageLoading] = useState(false)

  const loadCosts = useCallback(async () => {
    if (!channel) return
    setLoading(true)
    try {
      const data = await listChannelCosts(channel.id)
      setRows(
        data.items.map((item) => ({
          key: item.id,
          model: item.model,
          prompt: quotaToYuanInput(item.prompt_price, quotaPerYuan),
          cache: quotaToYuanInput(item.cache_price, quotaPerYuan),
          completion: quotaToYuanInput(item.completion_price, quotaPerYuan),
          perCall: quotaToYuanInput(item.per_call_price, quotaPerYuan),
          remark: item.remark,
        })),
      )
      setDeclaredModels(data.declared_models ?? [])
    } catch (err) {
      toastError(err instanceof Error ? err.message : '进价加载失败')
    } finally {
      setLoading(false)
    }
  }, [channel, quotaPerYuan, toastError])

  const loadUsage = useCallback(async () => {
    if (!channel) return
    setUsageLoading(true)
    try {
      const data = await fetchChannelKeyUsage(channel.id)
      setUsage(data.items ?? [])
    } catch {
      // 用量估算是「录价参考」而非主流程：取不到时静默置空，不能挡住站长录价
      setUsage(null)
    } finally {
      setUsageLoading(false)
    }
  }, [channel])

  useEffect(() => {
    if (!open) return
    void loadCosts()
    void loadUsage()
  }, [open, loadCosts, loadUsage])

  /** 人民币输入 → 非负数字；空串按 0（表示该维度不收费），非法输入返回 null 由调用方拦截 */
  function parseYuan(raw: string): number | null {
    if (raw.trim() === '') return 0
    const value = Number(raw)
    return Number.isFinite(value) && value >= 0 ? value : null
  }

  /** 人民币 → 站内额度；比例缺失时按数值原样取整（与计价页同一兜底口径） */
  function toQuota(yuan: number): number {
    return yuanToQuota(yuan, quotaPerYuan) ?? Math.round(yuan)
  }

  function updateRow(key: number, patch: Partial<CostRow>) {
    setRows((prev) => prev.map((row) => (row.key === key ? { ...row, ...patch } : row)))
  }

  function removeRow(key: number) {
    setRows((prev) => prev.filter((row) => row.key !== key))
  }

  /** 用渠道声明的模型清单补全缺失行：模型名手敲必错（错一个字符规则就静默失效），
   *  后端下发 declared_models 正是为了「按渠道模型预填」，这里只补还没有进价行的模型。 */
  function fillDeclaredModels() {
    setRows((prev) => {
      const existing = new Set(prev.map((r) => r.model.trim()).filter(Boolean))
      const missing = declaredModels.filter((m) => !existing.has(m))
      if (missing.length === 0) return prev
      return [...prev, ...missing.map((m) => emptyRow(m))]
    })
  }

  async function handleSave() {
    if (!channel) return
    const items: ChannelModelCostPayload[] = []
    for (const row of rows) {
      const model = row.model.trim()
      // 模型名为空的行直接跳过（后端同样忽略）：给「新增一行」留的草稿行不该卡住整批保存
      if (!model) continue
      const prompt = parseYuan(row.prompt)
      const cache = parseYuan(row.cache)
      const completion = parseYuan(row.completion)
      const perCall = parseYuan(row.perCall)
      if (prompt === null || cache === null || completion === null || perCall === null) {
        toastError(`「${model}」的价格必须是 ≥0 的数字`)
        return
      }
      items.push({
        model,
        prompt_price: toQuota(prompt),
        cache_price: toQuota(cache),
        completion_price: toQuota(completion),
        per_call_price: toQuota(perCall),
        remark: row.remark.trim(),
      })
    }
    // 同一模型录两行会让「取哪条」变成未定义行为，保存前拦下（后端也会拒绝，但前端报错更快）
    if (items.length !== new Set(items.map((i) => i.model)).size) {
      toastError('存在重复的模型名，请合并后再保存')
      return
    }
    setSaving(true)
    try {
      const result = await saveChannelCosts(channel.id, items)
      toast(`进价已保存：新增 ${result.created} 条、更新 ${result.updated} 条`)
      await loadCosts()
      // 进价变了，估算消耗随之变化：重拉一次让站长立刻看到新口径下的剩余
      await loadUsage()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  // 尚未录价的声明模型数：决定「按渠道模型补全」按钮是否值得出现
  const missingDeclared = declaredModels.filter(
    (m) => !rows.some((r) => r.model.trim() === m),
  ).length
  // 汇总「已产生用量但未录进价」的模型：这是成本口径下最危险的漏项
  const unpricedModels = Array.from(new Set((usage ?? []).flatMap((k) => k.unpriced_models)))

  return (
    <Modal open={open} onClose={onClose} title={`上游进价 · ${channel?.name ?? ''}`} width={980}>
      <div className="space-y-4">
        <p className="text-[13px] text-ink-3">
          录入上游对每个模型的收费（口径与站内售价一致：每 100 万 token 的价格，按次计费的模型只填按次价）。
          保存为整组替换——删除行后保存即真实删除。四个价格全为 0 表示「上游免费」。
        </p>

        <div className="flex flex-wrap items-center gap-2">
          <Button variant="secondary" size="sm" onClick={() => setRows((prev) => [...prev, emptyRow()])}>
            新增一行
          </Button>
          {missingDeclared > 0 && (
            <Button variant="secondary" size="sm" onClick={fillDeclaredModels}>
              补全缺失模型（{missingDeclared} 个）
            </Button>
          )}
          {rows.length > 0 && <span className="text-xs text-ink-3">共 {rows.length} 条</span>}
        </div>

        {loading ? (
          <SkeletonRows rows={3} />
        ) : (
          <div className="overflow-hidden rounded-md border border-line">
            <div className="overflow-x-auto">
              <table className="w-full border-collapse text-[13px]">
                <thead>
                  <tr className="border-b border-line bg-surface/70 text-ink-3">
                    <th className="px-2 py-2 text-left font-medium">模型</th>
                    <th className="px-2 py-2 text-left font-medium">输入价（¥/M）</th>
                    <th className="px-2 py-2 text-left font-medium">缓存价（¥/M）</th>
                    <th className="px-2 py-2 text-left font-medium">输出价（¥/M）</th>
                    <th className="px-2 py-2 text-left font-medium">按次价（¥/次）</th>
                    <th className="px-2 py-2 text-left font-medium">备注</th>
                    <th className="px-2 py-2 text-right font-medium">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={row.key} className="border-b border-line/70 last:border-0">
                      <td className="px-2 py-1.5">
                        <Input
                          value={row.model}
                          onChange={(e) => updateRow(row.key, { model: e.target.value })}
                          className="min-w-44"
                          placeholder="模型名"
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <Input
                          value={row.prompt}
                          onChange={(e) => updateRow(row.key, { prompt: e.target.value })}
                          className="tabular-nums"
                          type="number"
                          min={0}
                          step="0.000001"
                          placeholder="0"
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <Input
                          value={row.cache}
                          onChange={(e) => updateRow(row.key, { cache: e.target.value })}
                          className="tabular-nums"
                          type="number"
                          min={0}
                          step="0.000001"
                          placeholder="0"
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <Input
                          value={row.completion}
                          onChange={(e) => updateRow(row.key, { completion: e.target.value })}
                          className="tabular-nums"
                          type="number"
                          min={0}
                          step="0.000001"
                          placeholder="0"
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <Input
                          value={row.perCall}
                          onChange={(e) => updateRow(row.key, { perCall: e.target.value })}
                          className="tabular-nums"
                          type="number"
                          min={0}
                          step="0.000001"
                          placeholder="0"
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <Input
                          value={row.remark}
                          onChange={(e) => updateRow(row.key, { remark: e.target.value })}
                          className="min-w-32"
                          placeholder="可选"
                        />
                      </td>
                      <td className="px-2 py-1.5 text-right">
                        <button
                          type="button"
                          onClick={() => removeRow(row.key)}
                          className="text-[13px] text-ink-3 hover:text-err"
                        >
                          删除
                        </button>
                      </td>
                    </tr>
                  ))}
                  {rows.length === 0 && (
                    <tr>
                      <td colSpan={7} className="px-3 py-6 text-center text-ink-3">
                        尚无进价记录；未录进价的模型在对账页按 0 成本计算
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        )}

        {/* ── 按密钥用量估算：帮助站长定进价与更新余额快照 ── */}
        <div className="border-t border-line pt-4">
          <h4 className="text-[13px] font-semibold text-ink">按密钥用量估算</h4>
          <p className="mt-0.5 text-xs text-ink-3">
            估算 = 累计用量 × 上表进价（未录进价的模型不计入）。「剩余」= 人工录入的余额快照 − 估算消耗，
            为负表示按当前进价已用超；余额不会自动更新，需站长据此手动修订。
          </p>

          {unpricedModels.length > 0 && (
            <div className="mt-2 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-[13px] text-warn">
              以下 {unpricedModels.length} 个模型已产生用量但未录进价，其成本正按 0 计算：
              <span className="break-all">{unpricedModels.join('、')}</span>
            </div>
          )}

          <div className="mt-3">
            {usageLoading ? (
              <SkeletonRows rows={2} />
            ) : !usage || usage.length === 0 ? (
              <div className="rounded-md border border-line px-3 py-4 text-center text-[13px] text-ink-3">
                该渠道暂无密钥用量数据
              </div>
            ) : (
              <div className="overflow-hidden rounded-md border border-line">
                <div className="overflow-x-auto">
                  <table className="w-full border-collapse text-[13px]">
                    <thead>
                      <tr className="border-b border-line bg-surface/70 text-ink-3">
                        <th className="px-3 py-2 text-left font-medium">密钥</th>
                        <th className="px-3 py-2 text-left font-medium">状态</th>
                        <th className="px-3 py-2 text-right font-medium">录入余额</th>
                        <th className="px-3 py-2 text-right font-medium">估算消耗</th>
                        <th className="px-3 py-2 text-right font-medium">估算剩余</th>
                        <th className="px-3 py-2 text-right font-medium">未录进价</th>
                      </tr>
                    </thead>
                    <tbody>
                      {usage.map((k) => (
                        <tr key={k.channel_key_id} className="border-b border-line/70 last:border-0">
                          <td className="max-w-56 px-3 py-2">
                            <span className="block truncate text-ink-2" title={k.label || k.account_hint}>
                              {k.label || k.account_hint || `密钥 #${k.channel_key_id}`}
                            </span>
                          </td>
                          <td className="px-3 py-2">
                            {k.status === KEY_STATUS_ENABLED ? (
                              <Badge tone="ok">启用</Badge>
                            ) : k.status === KEY_STATUS_AUTO_REMOVED ? (
                              <Badge tone="err">已摘除</Badge>
                            ) : (
                              <Badge tone="off">禁用</Badge>
                            )}
                          </td>
                          <td className="px-3 py-2 text-right align-top tabular-nums">
                            {k.balance_known ? (
                              formatYuanFromQuota(k.balance, quotaPerYuan)
                            ) : (
                              <span className="text-ink-3">未录入</span>
                            )}
                          </td>
                          <td className="px-3 py-2 text-right align-top tabular-nums text-ink-2">
                            {formatYuanFromQuota(k.estimated_cost, quotaPerYuan)}
                          </td>
                          <td className="px-3 py-2 text-right align-top tabular-nums">
                            {k.balance_known ? (
                              // 负剩余是站长最需要看到的信号（比显示 0 或隐藏更有信息量）
                              k.remaining < 0 ? (
                                <span className="text-err">{formatYuanFromQuota(k.remaining, quotaPerYuan)}</span>
                              ) : (
                                <span className="text-ink-2">{formatYuanFromQuota(k.remaining, quotaPerYuan)}</span>
                              )
                            ) : (
                              <span className="text-ink-3">—</span>
                            )}
                          </td>
                          <td
                            className="px-3 py-2 text-right align-top tabular-nums"
                            title={k.unpriced_models.join('、')}
                          >
                            {k.unpriced_models.length > 0 ? (
                              <span className="text-warn">{k.unpriced_models.length} 个模型</span>
                            ) : (
                              <span className="text-ink-3">—</span>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            )}
          </div>
        </div>

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            关闭
          </Button>
          <Button variant="primary" loading={saving} onClick={handleSave}>
            保存进价（整组替换）
          </Button>
        </div>
      </div>
    </Modal>
  )
}
