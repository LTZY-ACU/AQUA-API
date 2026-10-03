/** 管理后台：告警通道（/admin/alert-channels）。
 *
 * 意图（Why）：
 *   渠道熔断、成功率自动停用、账号锁定这些事件此前只落服务端日志，
 *   "站点出事了"要靠主动翻日志才发现——凌晨渠道全挂、早上才处理，
 *   是中转站最常见也最贵的故障形态。本页把外发通道显式管起来，
 *   目标只有一个：让故障在几秒内出现在站长手机上。
 *
 * 三处刻意的设计：
 *   1) 投递目标永远只显示脱敏值。钉钉/企微的 URL 自带 access_token，
 *      一次截图或一次日志粘贴就等于把机器人凭据送出去；
 *   2) 新建/修改/删除/测试四个动作全部走二次验证（useReauthGuard）——
 *      能改告警目标的人，等于能让本站把内部事件发到他指定的任意地址；
 *   3) 「发送测试」同步等结果。让管理员当场知道地址对不对，
 *      而不是等到真出事才发现告警根本没到。
 *
 * 流转（Flow）：
 *   本页 → listAlertChannels（列表）/ listAlertChannelKinds（类型与事件目录）
 *        → create|update|delete|testAlertChannel（均在 guard 内）
 *
 * 扩展（Extend）：
 *   后端新增通道类型或事件类型时，本页无需改动——
 *   下拉框内容全部来自 /alert-channel-kinds。
 */
'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'

import {
  createAlertChannel,
  deleteAlertChannel,
  listAlertChannelKinds,
  listAlertChannels,
  testAlertChannel,
  updateAlertChannel,
  type AlertChannel,
  type AlertChannelKinds,
} from '@/api/alert'
import { useReauthGuard } from '@/components/auth/ReauthGuard'
import { Badge, Card } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch } from '@/components/ui/Form'
import { ConfirmDialog, Modal } from '@/components/ui/Modal'
import { DataTable, type Column } from '@/components/ui/Table'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

export default function AdminAlertChannelsPage() {
  const { toast, toastError } = useToast()
  const { guard, dialog: reauthDialog } = useReauthGuard()

  const [items, setItems] = useState<AlertChannel[]>([])
  const [catalog, setCatalog] = useState<AlertChannelKinds | null>(null)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<AlertChannel | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<AlertChannel | null>(null)
  // 正在执行测试 / 切换启停的通道 id（0 = 无），用于按钮置灰防重复点击
  const [busyId, setBusyId] = useState(0)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [list, kinds] = await Promise.all([listAlertChannels(), listAlertChannelKinds()])
      setItems(list.items)
      setCatalog(kinds)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '告警通道加载失败')
    } finally {
      setLoading(false)
    }
  }, [toastError])

  useEffect(() => {
    void load()
  }, [load])

  const enabledCount = useMemo(() => items.filter((i) => i.enabled).length, [items])

  /** 发送测试：走 guard，403 时弹密码框并自动重试 */
  async function handleTest(row: AlertChannel) {
    setBusyId(row.id)
    try {
      const result = await guard(() => testAlertChannel(row.id))
      toast(result.message || '测试告警已发送，请检查目标是否收到')
    } catch (err) {
      toastError(err instanceof Error ? err.message : '测试发送失败')
    } finally {
      setBusyId(0)
    }
  }

  /** 就地启停：停用是"暂时别发"，不是删配置，因此不发测试也不提示确认 */
  async function handleToggleEnabled(row: AlertChannel) {
    setBusyId(row.id)
    try {
      await guard(() =>
        updateAlertChannel(row.id, {
          name: row.name,
          kind: row.kind,
          events: row.events.join(','),
          enabled: !row.enabled,
        }),
      )
      toast(row.enabled ? '通道已停用' : '通道已启用')
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '启停更新失败')
    } finally {
      setBusyId(0)
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await guard(() => deleteAlertChannel(deleteTarget.id))
      toast('告警通道已删除')
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '删除失败')
    }
  }

  /** 事件订阅的展示：空 = 订阅全部，必须显式说明而不是显示"—" */
  function renderEvents(row: AlertChannel) {
    if (!catalog) return <span className="text-ink-3">—</span>
    if (row.events.length === 0) {
      return <Badge tone="brand">全部事件</Badge>
    }
    const textMap = new Map(catalog.events.map((e) => [e.value, e.text]))
    return (
      <span className="flex flex-wrap gap-1">
        {row.events.map((key) => (
          <Badge key={key} tone="info">
            {textMap.get(key) ?? key}
          </Badge>
        ))}
      </span>
    )
  }

  const columns: Column<AlertChannel>[] = [
    {
      title: '通道',
      render: (row) => (
        <div className="min-w-0">
          <div className="truncate text-[13px] font-medium text-ink" title={row.name}>
            {row.name}
          </div>
          <div className="mt-0.5 text-[12px] text-ink-3">{row.kind_text}</div>
        </div>
      ),
    },
    {
      title: '投递目标',
      // 脱敏值在这里是唯一形态：不提供"显示明文"，因为根本没有明文可显示。
      render: (row) => (
        <code className="block max-w-72 truncate text-[12px] text-ink-2" title={row.target_masked}>
          {row.target_masked}
        </code>
      ),
    },
    {
      title: '订阅事件',
      render: renderEvents,
    },
    {
      title: '启用',
      align: 'center',
      render: (row) => (
        <div className="flex justify-center">
          <Switch
            checked={row.enabled}
            disabled={busyId === row.id}
            onChange={() => handleToggleEnabled(row)}
            label={`启用通道 ${row.name}`}
          />
        </div>
      ),
    },
    {
      title: '更新时间',
      className: 'hidden lg:table-cell',
      render: (row) => (
        <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.updated_at)}</span>
      ),
    },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-3 text-[13px]">
          <button
            type="button"
            disabled={busyId === row.id}
            onClick={() => handleTest(row)}
            className="text-brand hover:underline disabled:opacity-50"
          >
            发送测试
          </button>
          <button
            type="button"
            disabled={busyId === row.id}
            onClick={() => setEditing(row)}
            className="text-ink-3 hover:text-ink disabled:opacity-50"
          >
            编辑
          </button>
          <button
            type="button"
            disabled={busyId === row.id}
            onClick={() => setDeleteTarget(row)}
            className="text-ink-3 hover:text-err disabled:opacity-50"
          >
            删除
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">告警通道</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">
            渠道熔断、自动停用、账号锁定时自动通知 · 共 {items.length} 条，启用 {enabledCount} 条
          </p>
        </div>
        <div className="flex items-center gap-3">
          <Button variant="secondary" onClick={() => void load()} loading={loading}>
            刷新
          </Button>
          <Button variant="primary" onClick={() => setCreating(true)} disabled={!catalog}>
            新建通道
          </Button>
        </div>
      </div>

      <AlertEventExplainer catalog={catalog} />

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle="还没有配置告警通道"
          emptyDescription="未配置时事件只记录在服务端日志里。至少配一条邮件通道，可在站点出问题时第一时间收到通知。"
        />
      </Card>

      <ChannelModal
        open={creating || editing !== null}
        channel={editing}
        catalog={catalog}
        onClose={() => {
          setCreating(false)
          setEditing(null)
        }}
        onSubmit={async (payload) => {
          if (editing) {
            await guard(() => updateAlertChannel(editing.id, payload))
            toast('告警通道已更新')
          } else {
            await guard(() => createAlertChannel(payload))
            toast('告警通道已创建')
          }
          setCreating(false)
          setEditing(null)
          void load()
        }}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title="删除告警通道"
        message={`确认删除「${deleteTarget?.name}」？删除后该通道不再接收任何告警。`}
        danger
        confirmText="删除"
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />

      {reauthDialog}
    </div>
  )
}

/* ── 事件说明 ──────────────────────────────────────────────────────────── */

/**
 * 事件说明条：把"哪些事情会触发告警"直接摆在页面上。
 *
 * Why 不做成折叠：这是管理员唯一需要记住的配置知识。
 * 藏起来等于让人去翻文档，而文档往往不存在于自建站里。
 */
function AlertEventExplainer({ catalog }: { catalog: AlertChannelKinds | null }) {
  if (!catalog) return null
  return (
    <Card>
      <div className="mb-2 text-[13px] font-semibold text-ink">什么情况会触发告警</div>
      <ul className="space-y-1.5">
        {catalog.events
          .filter((e) => e.value !== 'test')
          .map((e) => (
            <li key={e.value} className="flex gap-2 text-[13px] text-ink-2">
              <span className="mt-1.5 h-1 w-1 shrink-0 rounded-full bg-ink-3" />
              <span>{e.text}</span>
            </li>
          ))}
      </ul>
      <p className="mt-3 text-[12px] leading-relaxed text-ink-3">
        同一事件在去重窗口内只通知一次（严重 3 分钟 / 警告 10 分钟 / 通知 30 分钟），
        避免渠道持续故障时刷屏。通道不订阅任何事件时视为订阅全部。
      </p>
    </Card>
  )
}

/* ── 新建 / 编辑弹层 ───────────────────────────────────────────────────── */

function ChannelModal({
  open,
  channel,
  catalog,
  onClose,
  onSubmit,
}: {
  open: boolean
  /** 非 null 表示编辑模式（此时 target 留空 = 沿用原值） */
  channel: AlertChannel | null
  catalog: AlertChannelKinds | null
  onClose: () => void
  onSubmit: (payload: {
    name: string
    kind: string
    target?: string
    events: string
    enabled: boolean
  }) => Promise<void>
}) {
  const { toastError } = useToast()
  const [name, setName] = useState('')
  const [kind, setKind] = useState('email')
  const [target, setTarget] = useState('')
  const [events, setEvents] = useState<string[]>([])
  const [enabled, setEnabled] = useState(true)
  const [loading, setLoading] = useState(false)

  const isEdit = channel !== null

  useEffect(() => {
    if (!open) return
    setName(channel?.name ?? '')
    setKind(channel?.kind ?? 'email')
    setTarget('')
    setEvents(channel?.events ?? [])
    setEnabled(channel?.enabled ?? true)
  }, [open, channel])

  /** 目标输入框的占位与帮助文案随类型变化：邮箱和 URL 的校验规则完全不同 */
  const targetHint = useMemo(() => {
    if (kind === 'email') return '收件邮箱，例如 ops@example.com'
    if (kind === 'dingtalk') return '钉钉自定义机器人的 Webhook 地址（含 access_token）'
    if (kind === 'wecom') return '企业微信群机器人的 Webhook 地址（含 key）'
    return '接收告警的 HTTP 接口地址'
  }, [kind])

  async function handleSubmit() {
    if (!name.trim()) {
      toastError('请填写通道名称')
      return
    }
    // 编辑时目标留空是合法的（沿用原值），新建时必须填
    if (!isEdit && !target.trim()) {
      toastError('请填写投递目标')
      return
    }
    setLoading(true)
    try {
      await onSubmit({
        name: name.trim(),
        kind,
        target: target.trim() || undefined,
        // 空数组 → 空串 → 后端解释为"订阅全部"
        events: events.join(','),
        enabled,
      })
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setLoading(false)
    }
  }

  function toggleEvent(key: string) {
    setEvents((prev) => (prev.includes(key) ? prev.filter((e) => e !== key) : [...prev, key]))
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={isEdit ? '编辑告警通道' : '新建告警通道'}
      width={600}
    >
      <div className="space-y-4">
        <Field label="通道名称" required help="用于列表展示与告警正文，例如「值班邮箱」">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="给这条通道起个名字" />
        </Field>

        <Field label="通道类型" required>
          <Select value={kind} onChange={(e) => setKind(e.target.value)} disabled={isEdit}>
            {(catalog?.kinds ?? []).map((k) => (
              <option key={k.value} value={k.value}>
                {k.text}
              </option>
            ))}
          </Select>
        </Field>

        <Field
          label="投递目标"
          required={!isEdit}
          help={
            isEdit
              ? '出于安全考虑此处不回显原值。留空表示沿用当前目标；只有在更换地址时才需要填写。'
              : '目标会被加密保存，列表中只显示脱敏形式。钉钉/企微的地址自带凭据，请勿分享给非授权人。'
          }
        >
          <Input
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            placeholder={isEdit ? '留空 = 沿用原目标' : targetHint}
            autoComplete="off"
          />
        </Field>

        <Field
          label="订阅事件"
          help="全部取消勾选表示订阅全部事件（推荐：默认就该收全，少一次配置就少一次漏看）"
        >
          <div className="grid gap-2 rounded-md border border-line-2 bg-surface p-3 sm:grid-cols-2">
            {(catalog?.events ?? [])
              .filter((e) => e.value !== 'test')
              .map((e) => (
                <label key={e.value} className="flex items-center gap-2 text-[13px] text-ink-2">
                  <input
                    type="checkbox"
                    checked={events.includes(e.value)}
                    onChange={() => toggleEvent(e.value)}
                    className="h-3.5 w-3.5 accent-[var(--brand)]"
                  />
                  {e.text}
                </label>
              ))}
            {events.length === 0 && (
              <p className="text-[12px] text-ink-3 sm:col-span-2">
                当前：订阅全部事件
              </p>
            )}
          </div>
        </Field>

        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>创建后立即启用</span>
          <Switch checked={enabled} onChange={setEnabled} label="创建后立即启用" />
        </label>
      </div>

      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          取消
        </Button>
        <Button variant="primary" loading={loading} onClick={() => void handleSubmit()}>
          {isEdit ? '保存' : '创建'}
        </Button>
      </div>
    </Modal>
  )
}