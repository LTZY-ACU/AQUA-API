/** 管理后台：渠道管理（/admin/channels）。
 *
 * 意图（Why）：
 *   渠道是网关的「上游接入点」。列表 + 新建/编辑弹层 + 测活 + 启停/删除。
 *   表单按渠道类型目录（fetchChannelTypes）做触发式渲染，密钥池支持批量粘贴。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import {
  createChannel,
  deleteChannel,
  fetchChannelTypes,
  listChannels,
  testChannel,
  updateChannel,
} from '@/api/admin'
import type { Channel, ChannelPayload, ChannelTestResult, ChannelType } from '@/api/types'
import { Badge, Card, EmptyState, Tabs } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch, Textarea } from '@/components/ui/Form'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { useToast } from '@/lib/toast/toast-context'
import { channelStatusLabel, channelStatusBadgeClass } from '@/utils/display'
import { formatDateTime, formatLatency, formatRelative } from '@/utils/format'

const PAGE_SIZE = 20

export default function AdminChannelsPage() {
  const [items, setItems] = useState<Channel[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [types, setTypes] = useState<ChannelType[]>([])
  const [editing, setEditing] = useState<Channel | null | 'new'>(null)
  const [deleteTarget, setDeleteTarget] = useState<Channel | null>(null)
  const [testResult, setTestResult] = useState<ChannelTestResult | null>(null)
  const [testing, setTesting] = useState(false)

  const { toast, toastError } = useToast()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listChannels({ page, size: PAGE_SIZE })
      setItems(data.items)
      setTotal(data.total)
    } catch {
      /* 401 统一处理 */
    } finally {
      setLoading(false)
    }
  }, [page])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    void fetchChannelTypes().then((data) => setTypes(data.items)).catch(() => setTypes([]))
  }, [])

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteChannel(deleteTarget.id)
      toast('渠道已删除')
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '删除失败')
    }
  }

  async function handleToggle(channel: Channel) {
    try {
      await updateChannel(channel.id, { status: channel.status === 1 ? 2 : 1 } as Partial<ChannelPayload>)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '操作失败')
    }
  }

  async function handleTest(channel: Channel) {
    setTesting(true)
    setTestResult(null)
    try {
      const result = await testChannel(channel.id)
      setTestResult(result)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '测活失败')
    } finally {
      setTesting(false)
    }
  }

  const columns: Column<Channel>[] = [
    { title: '名称', render: (row) => <span className="font-medium text-ink">{row.name}</span> },
    { title: '类型', render: (row) => <span className="text-ink-2">{row.type_key || `#${row.type}`}</span> },
    {
      title: '模型',
      render: (row) => <span className="max-w-44 truncate text-[13px] text-ink-3" title={row.models.join(', ')}>{row.models.length > 0 ? `${row.models.length} 个模型` : '全部模型'}</span>,
    },
    {
      title: '状态',
      render: (row) => (
        <span className={channelStatusBadgeClass(row.status)}>{channelStatusLabel(row.status)}</span>
      ),
    },
    { title: '延迟 / 测活', render: (row) => <ProbeCell channel={row} /> },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => handleTest(row)} className="text-ink-3 hover:text-brand" disabled={testing}>测活</button>
          <button type="button" onClick={() => setEditing(row)} className="text-ink-3 hover:text-brand">编辑</button>
          <button type="button" onClick={() => handleToggle(row)} className="text-ink-3 hover:text-brand">{row.status === 1 ? '停用' : '启用'}</button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">删除</button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">渠道管理</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">上游接入渠道（{total}）</p>
        </div>
        <Button variant="primary" onClick={() => setEditing('new')}>新建渠道</Button>
      </div>

      <Card padding="none">
        <DataTable columns={columns} rows={loading ? null : items} loading={loading} rowKey={(row) => row.id} emptyTitle="还没有渠道" />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      {/* 测活结果弹层 */}
      <Modal open={Boolean(testResult)} onClose={() => setTestResult(null)} title="渠道测活结果" width={560}>
        {testResult && (
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              {testResult.ok ? <Badge tone="ok">通过</Badge> : <Badge tone="err">失败</Badge>}
              <span className="text-sm text-ink-2">{testResult.message}</span>
            </div>
            {testResult.latency_ms > 0 && <div className="text-[13px] text-ink-3">耗时 {testResult.latency_ms}ms</div>}
            {testResult.upstream_body && (
              <div className="rounded-md border border-line bg-surface p-3 text-xs text-ink-2">
                <div className="mb-1 font-medium text-ink-3">上游响应</div>
                <code className="break-all">{testResult.upstream_body}</code>
              </div>
            )}
          </div>
        )}
      </Modal>

      <ChannelFormModal
        open={editing !== null}
        channel={editing === 'new' ? null : editing}
        types={types}
        onClose={() => setEditing(null)}
        onSaved={() => { setEditing(null); void load() }}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title="删除渠道"
        message={`确认删除渠道「${deleteTarget?.name}」？删除后该渠道的调用将立即失败。`}
        danger
        confirmText="删除"
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/* ── 巡检结论单元格 ─────────────────────────────────────── */

/**
 * 把「最近一次测活」压缩成一格展示。
 *
 * 时间必须与延迟同时出现：延迟是一次测量而非均值，三小时前的 80ms
 * 说明不了现在的状况，只给数字会被误读成"实时延迟"。
 *
 * 状态码要翻译成动作：401/404/429 各自对应完全不同的处置方式，
 * 直接摆一个 401 出来，管理员还得自己回忆这代表密钥坏了还是模型没了。
 */
function ProbeCell({ channel }: { channel: Channel }) {
  const tested = Boolean(channel.last_test_at) || Boolean(channel.latency_ms)
  if (!tested) {
    return (
      <span className="text-ink-2" title="后台巡检会在下一个周期自动测活，也可以现在手动点「测活」">
        <Badge tone="off">未测</Badge>
      </span>
    )
  }
  const ok = Boolean(channel.last_test_ok)
  const tooltip = [
    channel.last_test_at
      ? `测于 ${formatDateTime(channel.last_test_at)}（${formatRelative(channel.last_test_at)}）`
      : '测于未知时间',
    channel.last_test_model ? `模型 ${channel.last_test_model}` : '',
    channel.last_test_code ? `HTTP ${channel.last_test_code}` : '网络层未连通',
    probeHint(channel),
  ]
    .filter(Boolean)
    .join('\n')
  return (
    <span className="flex items-center gap-1.5" title={tooltip}>
      <Badge tone={ok ? 'ok' : 'err'}>{ok ? '正常' : '异常'}</Badge>
      <span className="text-ink-2">{channel.latency_ms ? formatLatency(channel.latency_ms) : '—'}</span>
    </span>
  )
}

/** 状态码 → 下一步该做什么（只覆盖常见且处置方式明确的几种，其余不臆测） */
function probeHint(channel: Channel): string {
  const code = channel.last_test_code ?? 0
  if (code === 0) return '连不上：检查上游地址、出网与 DNS'
  if (code === 401 || code === 403) return '鉴权失败：上游密钥无效或已过期，换一把'
  if (code === 404) return '模型不存在：清理渠道里的模型名或改用上游真实模型'
  if (code === 429) return '被限流：降低调用频率或补充密钥池'
  if (code >= 500) return '上游故障：等待对方恢复'
  return ''
}

/* ── 渠道表单弹层 ───────────────────────────────────────── */

function ChannelFormModal({
  open,
  channel,
  types,
  onClose,
  onSaved,
}: {
  open: boolean
  channel: Channel | null
  types: ChannelType[]
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const [name, setName] = useState('')
  const [type, setType] = useState(1)
  const [typeKey, setTypeKey] = useState('')
  const [baseUrl, setBaseUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [modelsText, setModelsText] = useState('')
  const [keysText, setKeysText] = useState('')
  const [status, setStatus] = useState(1)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setName(channel?.name ?? '')
    setType(channel?.type ?? 1)
    // 用 typeKey 做类型下拉的受控值：优先取目录中匹配的 key（编辑时 type 可能对应历史编号）
    const initialType = channel?.type ?? 1
    setType(initialType)
    setTypeKey(types.find((t) => t.key === String(initialType))?.key ?? types[0]?.key ?? '')
    setBaseUrl(channel?.base_url ?? '')
    setApiKey('')
    setModelsText((channel?.models ?? []).join('\n'))
    setKeysText('')
    setStatus(channel?.status ?? 1)
  }, [open, channel, types])

  const selectedType = types.find((t) => t.key === typeKey) ?? types[0]

  async function handleSubmit() {
    if (!name.trim()) {
      toastError('请填写渠道名称')
      return
    }
    if (!channel && !apiKey.trim()) {
      toastError('新建渠道必须填写上游密钥')
      return
    }
    if (!channel && !baseUrl.trim()) {
      toastError('新建渠道必须填写上游地址')
      return
    }
    setLoading(true)
    try {
      const payload: ChannelPayload = {
        name: name.trim(),
        type,
        type_key: typeKey || undefined,
        base_url: baseUrl.trim(),
        api_key: apiKey.trim() || undefined,
        models: modelsText.split('\n').map((s) => s.trim()).filter(Boolean),
        group: 'default',
        groups: ['default'],
        priority: 0,
        weight: 0,
        status,
        keys_text: keysText.trim() || undefined,
      }
      if (channel) {
        delete payload.api_key
        delete payload.keys_text
        await updateChannel(channel.id, payload)
        toast('渠道已更新')
      } else {
        await createChannel(payload)
        toast('渠道已创建')
      }
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={channel ? '编辑渠道' : '新建渠道'} width={640}>
      <div className="space-y-4">
        <Field label="渠道名称" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="给这个上游起个名字" />
        </Field>

        <Field label="渠道类型" help={selectedType?.notes}>
          {types.length > 0 ? (
            <Select value={typeKey} onChange={(e) => setTypeKey(e.target.value)}>
              {types.map((t) => (
                <option key={t.key} value={t.key} disabled={!t.available}>{t.label}</option>
              ))}
            </Select>
          ) : (
            <Input value={type} onChange={(e) => setType(Number(e.target.value))} type="number" placeholder="渠道类型编号" />
          )}
        </Field>

        <Field label="上游地址" required={!channel}>
          <Input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder={selectedType?.default_base_url || 'https://api.example.com'} />
        </Field>

        <Field label="上游密钥" required={!channel} help={channel ? '留空表示不修改' : '同时支持密钥池：每行一把，可用空格或逗号附备注'}>
          <Textarea value={apiKey} onChange={(e) => setApiKey(e.target.value)} rows={2} placeholder="sk-..." />
        </Field>

        {!channel && (
          <Field label="密钥池（可选）" help="每行一把，批量粘贴多把上游密钥">
            <Textarea value={keysText} onChange={(e) => setKeysText(e.target.value)} rows={3} placeholder="sk-a\nsk-b 备注1" />
          </Field>
        )}

        <Field label="模型列表" help="每行一个；留空表示支持全部模型">
          <Textarea value={modelsText} onChange={(e) => setModelsText(e.target.value)} rows={5} placeholder="gpt-4o\nclaude-3-5-sonnet" />
        </Field>

        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>启用渠道</span>
          <Switch checked={status === 1} onChange={(v) => setStatus(v ? 1 : 2)} />
        </label>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>取消</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{channel ? '保存' : '创建'}</Button>
      </div>
    </Modal>
  )
}