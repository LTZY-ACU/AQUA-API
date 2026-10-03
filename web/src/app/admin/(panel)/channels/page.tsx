/** 管理后台：渠道管理（/admin/channels）。
 *
 * 意图（Why）：
 *   渠道是网关的「上游接入点」。列表 + 新建/编辑弹层 + 测活 + 启停/删除。
 *   表单按渠道类型目录（fetchChannelTypes）做触发式渲染，密钥池支持批量粘贴。
 *   编辑弹层还挂载三块渠道专属能力（后端早已就绪、此前前端零入口的断链）：
 *     ① 上游进价（列表「进价」按钮 → ChannelCostModal）——/admin/finance 对账页成本列的数据来源；
 *     ② 模型映射（弹层内 ChannelModelMappings 分区）——平台模型 ID ↔ 上游模型 ID 的改写规则；
 *     ③ 从上游拉取模型（模型列表旁按钮）——按已存渠道或裸地址拉清单，避免手抄模型名出错。
 *   表单还消费两个后端目录（此前同样零调用的封装）：
 *     ④ 调度策略 / 失败处置下拉（fetchKeyStrategies / fetchKeyFailurePolicies）——
 *        两项均为渠道级配置（落库 channels.key_strategy / key_failure_policy），
 *        选项 label 与说明文案全部由后端下发，前端不硬编码，新增策略时界面自动跟随。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import {
  createChannel,
  deleteChannel,
  fetchChannelTypes,
  fetchKeyFailurePolicies,
  fetchKeyStrategies,
  fetchUpstreamModels,
  listChannels,
  testChannel,
  updateChannel,
} from '@/api/admin'
import type {
  Channel,
  ChannelPayload,
  ChannelTestResult,
  ChannelType,
  FetchModelsPayload,
  KeyStrategyOption,
} from '@/api/types'
import { ChannelCostModal } from '@/components/admin/ChannelCostModal'
import { ChannelHealthPanel } from '@/components/admin/ChannelHealthPanel'
import { ChannelKeyPool } from '@/components/admin/ChannelKeyPool'
import { ChannelModelMappings } from '@/components/admin/ChannelModelMappings'
import { SpeedTestModal } from '@/components/admin/SpeedTestModal'
import { Badge, Card, EmptyState, Tabs } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch, Textarea } from '@/components/ui/Form'
import { Modal, ConfirmDialog, CopyButton } from '@/components/ui/Modal'
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
  // 凭据调度/失败处置策略目录：与 types 同一模式——页面加载时拉一次缓存供表单下拉复用。
  // 文案由后端下发，前端不硬编码（后端新增策略时这里无需任何改动）。
  const [strategies, setStrategies] = useState<KeyStrategyOption[]>([])
  const [failurePolicies, setFailurePolicies] = useState<KeyStrategyOption[]>([])
  const [editing, setEditing] = useState<Channel | null | 'new'>(null)
  const [deleteTarget, setDeleteTarget] = useState<Channel | null>(null)
  const [testResult, setTestResult] = useState<ChannelTestResult | null>(null)
  const [testing, setTesting] = useState(false)
// 「上游进价」编辑弹层的目标渠道：/admin/finance 对账页的成本数据全靠它录入
  const [costTarget, setCostTarget] = useState<Channel | null>(null)
  // 模型测速弹层的目标渠道（null = 关闭）：与测活分开，两者口径不同
  //（测活=通不通；测速=每个模型各有多快）。
  const [speedTarget, setSpeedTarget] = useState<Channel | null>(null)

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
    // 两个目录都是稳定枚举（只读、无分页），失败时静默降级为空数组：
    // 表单对空目录有兜底渲染（见 ChannelFormModal），不该因目录拉不下来就打断渠道管理主流程。
    void fetchKeyStrategies().then((data) => setStrategies(data.items)).catch(() => setStrategies([]))
    void fetchKeyFailurePolicies().then((data) => setFailurePolicies(data.items)).catch(() => setFailurePolicies([]))
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
      // 密钥池运行态的"一览"信号：以「启用/总数」+ 异常数提示哪条渠道的凭据在掉队。
      // 元数据来自渠道列表响应里的 key_pool 概览（后端一次 GROUP BY 下发，无额外请求）。
      title: '密钥池',
      align: 'right',
      width: 'hidden sm:table-cell',
      className: 'hidden sm:table-cell',
      render: (row) => {
        const pool = row.key_pool
        if (!pool || pool.total === 0) return <span className="text-[13px] text-ink-3">单密钥</span>
        const degraded = pool.disabled + pool.auto_removed
        return (
          <span className="inline-flex items-center gap-2 text-[13px]">
            <span className="tabular-nums text-ink-2">{pool.enabled}/{pool.total}</span>
            {degraded > 0 && <Badge tone="warn">{degraded} 异常</Badge>}
          </span>
        )
      },
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
          <button type="button" onClick={() => setSpeedTarget(row)} className="text-ink-3 hover:text-brand" disabled={row.models.length === 0}>测速</button>
          <button type="button" onClick={() => handleTest(row)} className="text-ink-3 hover:text-brand" disabled={testing}>测活</button>
          <button type="button" onClick={() => setCostTarget(row)} className="text-ink-3 hover:text-brand">进价</button>
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

      {/* 健康概览：渠道状态分布 + 近 24h 全站调用健康度 */}
      <ChannelHealthPanel />

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

      {/* 模型测速弹层：逐模型测首字延迟。关闭时刷新列表——
          自动屏蔽（403/404）可能改了渠道的模型清单，需要反映到表格。 */}
      <SpeedTestModal
        open={speedTarget !== null}
        channelId={speedTarget?.id ?? null}
        channelName={speedTarget?.name}
        models={speedTarget?.models ?? []}
        onClose={() => {
          setSpeedTarget(null)
          void load()
        }}
      />

      <ChannelFormModal
        open={editing !== null}
        channel={editing === 'new' ? null : editing}
        types={types}
        strategies={strategies}
        failurePolicies={failurePolicies}
        onClose={() => setEditing(null)}
        onSaved={() => { setEditing(null); void load() }}
      />

      {/* 上游进价管理：渠道 × 模型的成本录入 + 按密钥用量估算（财务对账页的成本来源） */}
      <ChannelCostModal
        open={Boolean(costTarget)}
        channel={costTarget}
        onClose={() => setCostTarget(null)}
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
  strategies,
  failurePolicies,
  onClose,
  onSaved,
}: {
  open: boolean
  channel: Channel | null
  types: ChannelType[]
  /** 凭据调度策略目录（页面加载时拉取缓存，见页面组件） */
  strategies: KeyStrategyOption[]
  /** 密钥失败处置策略目录（同上） */
  failurePolicies: KeyStrategyOption[]
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
  // 凭据调度/失败处置（渠道级，落库 channels.key_strategy / key_failure_policy）：
  // 存的是后端目录里的稳定标识。编辑时回显当前值（后端保证 DTO 恒为合法值）；
  // 新建时留空提交 → 后端取默认策略（least_in_flight / cooldown_only）。
  const [keyStrategy, setKeyStrategy] = useState('')
  const [failurePolicy, setFailurePolicy] = useState('')
  const [loading, setLoading] = useState(false)
// 「从上游拉取模型」结果与进行中标记：拉取回来的清单先预览，确认后才填入模型列表
  const [fetchingModels, setFetchingModels] = useState(false)
  const [fetchedModels, setFetchedModels] = useState<string[] | null>(null)

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
    // 策略字段回显当前值：更新接口对这两个字段的语义是「留空表示不修改」，
    // 但编辑弹层既然展示了它们，就应该提交站长看到（且可改）的值，而不是靠留空隐式保留。
    setKeyStrategy(channel?.key_strategy ?? '')
    setFailurePolicy(channel?.key_failure_policy ?? '')
    // 每次打开弹层都清空上次拉取的清单：渠道可能已换地址，旧清单会误导
    setFetchedModels(null)
  }, [open, channel, types])

  const selectedType = types.find((t) => t.key === typeKey) ?? types[0]

/** 向上游拉取可用模型清单：模型名（如 meta/llama-3.1-70b-instruct）手抄必错一个字符整条路由就失效。
   *
   * 凭据策略（与后端 handleFetchModels 的两种模式对齐）：
   *   - 编辑态 + 未填新密钥 + 地址未改 → 传 channel_id，后端用库中地址与
   *     密钥（含密钥池兜底）拉取——这正是"渠道已配好，同步一下清单"的场景；
   *   - 其余情况（新建、或表单里改了地址/密钥）→ 传表单当前值，
   *     拉到的一定是"即将保存的这套配置"的真实清单。
   */
  async function handleFetchModels() {
    const url = baseUrl.trim()
    const key = apiKey.trim()
    const useSaved = channel != null && !key && url === channel.base_url
    if (!useSaved && !url) {
      toastError('请先填写上游地址')
      return
    }
    setFetchingModels(true)
    try {
      const payload: FetchModelsPayload = useSaved && channel
        ? { channel_id: channel.id }
        : { base_url: url, api_key: key || undefined }
      const data = await fetchUpstreamModels(payload)
      setFetchedModels(data.models ?? [])
      toast(`拉取到 ${data.count} 个模型，确认后点「填入模型列表」`)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '拉取失败')
    } finally {
      setFetchingModels(false)
    }
  }

  /** 把拉取结果合并进模型列表（去重保序），而不是覆盖：站长可能已手填了部分模型。 */
  function applyFetchedModels() {
    if (!fetchedModels || fetchedModels.length === 0) return
    const existing = modelsText.split('\n').map((s) => s.trim()).filter(Boolean)
    const seen = new Set(existing)
    const added = fetchedModels.filter((m) => !seen.has(m))
    setModelsText([...existing, ...added].join('\n'))
    toast(`已填入：新增 ${added.length} 个，当前共 ${existing.length + added.length} 个`)
  }
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
        // 空串时省略字段：新建走后端默认策略；编辑场景因回显恒非空，实际总会提交
        key_strategy: keyStrategy || undefined,
        key_failure_policy: failurePolicy || undefined,
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
    <Modal open={open} onClose={onClose} title={channel ? '编辑渠道' : '新建渠道'} width={760}>
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

        {/* 凭据调度/失败处置（渠道级配置，落库 channels.key_strategy / key_failure_policy）。
            选项与说明全部来自后端目录，前端不硬编码；说明文案随选中项联动显示在下方 help 位置。
            目录拉取失败（空数组）时退化为标识文本框，与渠道类型下拉的兜底模式一致——
            宁可手填原始标识，也不渲染一个没有任何选项的下拉。 */}
        <Field label="调度策略" help={strategies.find((s) => s.key === keyStrategy)?.description}>
          {strategies.length > 0 ? (
            <Select value={keyStrategy} onChange={(e) => setKeyStrategy(e.target.value)}>
              {/* 新建时才有「跟随默认」项：留空提交后端取默认策略；
                  编辑时当前值恒非空（回显保证），不需要这个隐式选项 */}
              {!channel && <option value="">跟随系统默认</option>}
              {strategies.map((s) => (
                <option key={s.key} value={s.key}>{s.label}</option>
              ))}
            </Select>
          ) : (
            <Input value={keyStrategy} onChange={(e) => setKeyStrategy(e.target.value)} placeholder="least_in_flight" />
          )}
        </Field>

        <Field label="失败处置" help={failurePolicies.find((p) => p.key === failurePolicy)?.description}>
          {failurePolicies.length > 0 ? (
            <Select value={failurePolicy} onChange={(e) => setFailurePolicy(e.target.value)}>
              {!channel && <option value="">跟随系统默认</option>}
              {failurePolicies.map((p) => (
                <option key={p.key} value={p.key}>{p.label}</option>
              ))}
            </Select>
          ) : (
            <Input value={failurePolicy} onChange={(e) => setFailurePolicy(e.target.value)} placeholder="cooldown_only" />
          )}
        </Field>

        <Field label="模型列表" help="每行一个；留空表示支持全部模型">
          {/* 一键从上游拉取：省去逐个手敲模型名（长名字极易敲错导致路由失效）。
              合并而非覆盖——已填写的条目可能是管理员刻意收敛过的子集。 */}
          <div className="mb-2 flex items-center justify-between gap-2">
            <span className="text-xs text-ink-3">支持从上游自动拉取清单，结果合并到下方</span>
            <Button
              variant="secondary"
              size="sm"
              loading={fetchingModels}
              onClick={handleFetchModels}
              disabled={!fetchingModels && !baseUrl.trim() && channel == null}
            >
              从上游获取
            </Button>
          </div>
          <Textarea value={modelsText} onChange={(e) => setModelsText(e.target.value)} rows={5} placeholder="gpt-4o\nclaude-3-5-sonnet" />
        </Field>

        {/* 从上游拉取模型：NIM 这类平台上架几百个模型，人工抄写必然出错 */}
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="secondary" size="sm" loading={fetchingModels} onClick={handleFetchModels}>
            从上游拉取模型
          </Button>
          <span className="text-xs text-ink-3">
            {channel ? '使用该渠道已保存的地址与密钥' : '使用上方填写的地址与密钥'}
          </span>
        </div>
        {fetchedModels && (
          <div className="rounded-md border border-line bg-surface p-3">
            <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
              <span className="text-ink-2">拉取到 {fetchedModels.length} 个模型</span>
              <span className="flex items-center gap-3">
                <button
                  type="button"
                  onClick={applyFetchedModels}
                  disabled={fetchedModels.length === 0}
                  className="text-brand hover:underline disabled:pointer-events-none disabled:opacity-50"
                >
                  填入模型列表
                </button>
                <CopyButton text={fetchedModels.join('\n')} label="复制清单" />
              </span>
            </div>
            <div className="mt-2 max-h-40 overflow-y-auto font-mono text-xs leading-5 text-ink-2">
              {fetchedModels.length > 0 ? (
                fetchedModels.map((m) => <div key={m}>{m}</div>)
              ) : (
                <span className="text-ink-3">上游返回了空清单</span>
              )}
            </div>
          </div>
        )}

        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>启用渠道</span>
          <Switch checked={status === 1} onChange={(v) => setStatus(v ? 1 : 2)} />
        </label>
      </div>

      {/* 模型映射维护：/admin/model-mappings 总览页注释写明「映射在渠道详情中维护」，
          本分区就是那个「渠道详情中的维护入口」。仅编辑已有渠道时展示（新建时还没有渠道 ID 可挂载）。 */}
      {channel && (
        <div className="mt-5 border-t border-line pt-4">
          <ChannelModelMappings channelId={channel.id} />
        </div>
      )}

      {/* 密钥池运行态：仅编辑已有渠道时展示（新建时还没有渠道 ID，没有明细可查）。
          放在表单下方独立分区，既不打乱"填表"的主线，又能就近观察凭据健康状况。 */}
      {channel && (
        <div className="mt-5 border-t border-line pt-4">
          <ChannelKeyPool channelId={channel.id} />
        </div>
      )}

      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>取消</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{channel ? '保存' : '创建'}</Button>
      </div>
    </Modal>
  )
}