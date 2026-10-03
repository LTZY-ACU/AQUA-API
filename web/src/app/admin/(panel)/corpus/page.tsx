/** 管理后台：语料共建（/admin/corpus）。
 *
 * 意图（Why）：
 *   后端「语料共建计划」的 10 个端点（清单/授权/样本/导出/统计）此前零界面，
 *   本页补齐管理闭环。语料是用户对话原文、本站最敏感的数据，因此界面刻意遵循
 *   后端定下的两条纪律：
 *     1) 列表只给 200 字预览，看全文必须点开单条——且每次查看/导出都会写审计日志，
 *        弹层里明确提示这一点，让管理员知道自己的行为有痕迹；
 *     2) 样本行不可整行点击（避免误触看全文产生审计噪音），只保留显式按钮入口。
 *
 * 流转（Flow）：
 *   页面（清单数据提升到本层共享）→ @/api/corpus → /api/admin/corpus/*
 *   四个分区：语料清单（upsert/启停/移除）· 样本浏览（筛选/分页/全文/JSONL 导出）
 *            统计总览（全库汇总 + 按模型样本量）· 福利授权（发放/撤销）
 *
 * 扩展（Extend）：
 *   样本筛选目前暴露「模型 + 用户 ID」；后端还支持 from/to 时间段（Unix 秒），
 *   需要时在 SamplesSection 的筛选栏加时间控件并同步传给 listCorpusSamples
 *   与 exportCorpusSamples（两处必须共用同一份筛选语义，与后端口径一致）。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import {
  deleteCorpusGrant,
  deleteCorpusModel,
  exportCorpusSamples,
  fetchCorpusStats,
  getCorpusSample,
  listCorpusGrants,
  listCorpusModels,
  listCorpusSamples,
  upsertCorpusGrant,
  upsertCorpusModel,
  type CorpusGrant,
  type CorpusModel,
  type CorpusSample,
  type CorpusSampleDetail,
  type CorpusStats,
} from '@/api/corpus'
import { Button } from '@/components/ui/Button'
import { Badge, Card, CodeBlock, SkeletonRows, StatCard, Tabs } from '@/components/ui/Display'
import { Field, Input, Select, Switch, Textarea } from '@/components/ui/Form'
import { ConfirmDialog, Modal } from '@/components/ui/Modal'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime, formatNumber } from '@/utils/format'

/** 样本列表每页条数：预览字段较宽，取 20 保证横向不溢出 */
const SAMPLES_PAGE_SIZE = 20

/** 字节数 → 可读体积（B/KB/MB…）：样本体积从几百字节到几 MB 跨多个数量级，裸数字难读 */
function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${unit === 0 || value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}

/** 样本正文大多是 JSON：格式化后方便人工核对；解析失败（截断/纯文本）则原样展示 */
function prettyJson(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

type TabKey = 'models' | 'samples' | 'stats' | 'grants'

export default function AdminCorpusPage() {
  const [tab, setTab] = useState<TabKey>('models')

  // 清单是三个分区共用的数据源（样本筛选下拉、授权模型选择、统计分列），
  // 提升到页面层只拉一份；清单任何变更后统一 reload。
  const [models, setModels] = useState<CorpusModel[] | null>(null)
  // 清单加载失败单独记状态而不是只 toast：503「模块未启用」是真实存在的状态，
  // 若只给空表格会让管理员误以为清单为空。
  const [modelsError, setModelsError] = useState('')

  const reloadModels = useCallback(async () => {
    setModelsError('')
    try {
      const data = await listCorpusModels()
      setModels(data.items)
    } catch (err) {
      setModels(null)
      setModelsError(err instanceof Error ? err.message : '语料清单加载失败')
    }
  }, [])

  useEffect(() => {
    void reloadModels()
  }, [reloadModels])

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">语料共建</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">
          维护采集清单与福利授权 · 样本全文查看与 JSONL 导出均会写入审计日志
        </p>
      </div>

      {modelsError && (
        <Card className="border-err/30">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-[13px] text-err">{modelsError}</p>
            <Button variant="secondary" size="sm" onClick={() => void reloadModels()}>
              重试
            </Button>
          </div>
        </Card>
      )}

      <Tabs<TabKey>
        items={[
          { value: 'models', label: '语料清单', count: models?.length },
          { value: 'samples', label: '样本浏览' },
          { value: 'stats', label: '统计总览' },
          { value: 'grants', label: '福利授权' },
        ]}
        value={tab}
        onChange={setTab}
      />

      {tab === 'models' && <ModelsSection models={models} onChanged={reloadModels} />}
      {tab === 'samples' && <SamplesSection models={models ?? []} />}
      {tab === 'stats' && <StatsSection models={models ?? []} />}
      {tab === 'grants' && <GrantsSection models={models ?? []} />}
    </div>
  )
}

/* ── 分区一：语料清单 ───────────────────────────────────── */

function ModelsSection({ models, onChanged }: { models: CorpusModel[] | null; onChanged: () => void }) {
  const { toast, toastError } = useToast()

  // 'new' = 新建；CorpusModel 对象 = 编辑该条
  const [editing, setEditing] = useState<CorpusModel | 'new' | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<CorpusModel | null>(null)
  // 正在就地切换启停的模型名（空串 = 无）：给对应行的 Switch 置灰防连点
  const [switching, setSwitching] = useState('')

  async function handleToggle(row: CorpusModel) {
    setSwitching(row.model)
    try {
      // upsert 是整条覆盖语义：切换启停必须把备注原样带回，否则 remark 会被清空
      await upsertCorpusModel({ model: row.model, enabled: !row.enabled, remark: row.remark })
      toast(row.enabled ? `已暂停采集 ${row.model}` : `已开始采集 ${row.model}`)
      onChanged()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '启停失败')
    } finally {
      setSwitching('')
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteCorpusModel(deleteTarget.model)
      toast('已从清单移除')
      setDeleteTarget(null)
      onChanged()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '移除失败')
    }
  }

  const columns: Column<CorpusModel>[] = [
    {
      title: '模型',
      render: (row) => <span className="text-[13px] font-medium text-ink">{row.model}</span>,
    },
    {
      title: '采集中',
      align: 'center',
      render: (row) => (
        <div className="flex justify-center">
          <Switch
            checked={row.enabled}
            disabled={switching === row.model}
            onChange={() => handleToggle(row)}
            label={`切换 ${row.model} 的采集状态`}
          />
        </div>
      ),
    },
    {
      title: '备注',
      render: (row) => (
        <span className="block max-w-64 truncate text-[13px] text-ink-3" title={row.remark}>
          {row.remark || '—'}
        </span>
      ),
    },
    {
      title: '更新时间',
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.updated_at)}</span>,
    },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-3 text-[13px]">
          <button type="button" className="text-ink-3 hover:text-brand" onClick={() => setEditing(row)}>
            编辑
          </button>
          <button type="button" className="text-ink-3 hover:text-err" onClick={() => setDeleteTarget(row)}>
            移除
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[13px] text-ink-3">共 {models?.length ?? 0} 个模型 · 清单改动立即生效，无需重启</p>
        <Button variant="primary" onClick={() => setEditing('new')}>
          新建语料
        </Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={models}
          rowKey={(row) => row.model}
          loading={models === null}
          emptyTitle="清单为空"
          emptyDescription="新建第一条语料后，命中该模型的转发请求会被采样入库。"
        />
      </Card>

      {editing !== null && (
        <ModelUpsertModal
          target={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            onChanged()
          }}
        />
      )}

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title="移除语料"
        message={`确认从清单移除「${deleteTarget?.model}」？已采集的历史样本不会被删除，只是停止后续采集。`}
        danger
        confirmText="移除"
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/** 清单项新建/编辑弹层：两者共用（upsert 语义，同名即整条覆盖） */
function ModelUpsertModal({
  target,
  onClose,
  onSaved,
}: {
  target: CorpusModel | 'new'
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const isEdit = target !== 'new'
  // 弹层只在打开时挂载，初始值直接取自 target，无需 useEffect 复位
  const [model, setModel] = useState(isEdit ? target.model : '')
  const [enabled, setEnabled] = useState(isEdit ? target.enabled : true)
  const [remark, setRemark] = useState(isEdit ? target.remark : '')
  const [loading, setLoading] = useState(false)

  async function handleSubmit() {
    const name = model.trim()
    if (!name) {
      toastError('请填写模型名')
      return
    }
    setLoading(true)
    try {
      await upsertCorpusModel({ model: name, enabled, remark: remark.trim() })
      toast(isEdit ? '语料已更新' : '语料已创建')
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={isEdit ? `编辑语料：${target.model}` : '新建语料'} width={560}>
      <div className="space-y-4">
        <Field label="模型名" required help="与转发请求里的 model 字段精确匹配才会被采样">
          <Input
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder="如 deepseek-v4.1-flash"
            disabled={isEdit}
          />
        </Field>
        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>启用采集</span>
          <Switch checked={enabled} onChange={setEnabled} label="启用采集" />
        </label>
        <Field label="备注">
          <Textarea value={remark} onChange={(e) => setRemark(e.target.value)} rows={2} placeholder="可选，如「为推理数据集采集」" />
        </Field>
        {isEdit && (
          <p className="text-xs text-ink-3">保存为整条覆盖：未提交的备注以本弹层内容为准。</p>
        )}
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          取消
        </Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>
          保存
        </Button>
      </div>
    </Modal>
  )
}

/* ── 分区二：样本浏览 ───────────────────────────────────── */

function SamplesSection({ models }: { models: CorpusModel[] }) {
  const { toast, toastError } = useToast()

  // 筛选用「草稿 → 点查询才生效」两段式：避免输入用户 ID 时逐字符触发查询
  const [modelDraft, setModelDraft] = useState('')
  const [userIdDraft, setUserIdDraft] = useState('')
  // model 用可选：初始空筛选是 {}，强制必填会让空对象过不了类型检查
  const [filter, setFilter] = useState<{ model?: string; user_id?: number }>({})
  const [page, setPage] = useState(1)
  const [items, setItems] = useState<CorpusSample[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)

  // 全文查看（独立于列表加载；每次打开都会在后端写一条审计日志）
  const [detailId, setDetailId] = useState<number | null>(null)
  const [detail, setDetail] = useState<CorpusSampleDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)

  // 导出（同样写审计日志，且包含对话原文，必须二次确认）
  const [confirmExport, setConfirmExport] = useState(false)
  const [exporting, setExporting] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const paged = await listCorpusSamples({
        model: filter.model || undefined,
        user_id: filter.user_id,
        page,
        size: SAMPLES_PAGE_SIZE,
      })
      setItems(paged.items)
      setTotal(paged.total)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '样本加载失败')
    } finally {
      setLoading(false)
    }
  }, [filter, page, toastError])

  useEffect(() => {
    void load()
  }, [load])

  function handleSearch() {
    const trimmed = userIdDraft.trim()
    if (trimmed && !/^\d+$/.test(trimmed)) {
      toastError('用户 ID 必须是纯数字')
      return
    }
    setFilter({ model: modelDraft, user_id: trimmed ? Number(trimmed) : undefined })
    setPage(1)
  }

  function handleReset() {
    setModelDraft('')
    setUserIdDraft('')
    setFilter({})
    setPage(1)
  }

  async function openDetail(id: number) {
    setDetailId(id)
    setDetail(null)
    setDetailLoading(true)
    try {
      setDetail(await getCorpusSample(id))
    } catch (err) {
      toastError(err instanceof Error ? err.message : '样本详情加载失败')
      setDetailId(null)
    } finally {
      setDetailLoading(false)
    }
  }

  async function handleExport() {
    setConfirmExport(false)
    setExporting(true)
    try {
      await exportCorpusSamples({ model: filter.model || undefined, user_id: filter.user_id })
      toast('JSONL 已开始下载')
    } catch (err) {
      toastError(err instanceof Error ? err.message : '导出失败')
    } finally {
      setExporting(false)
    }
  }

  const columns: Column<CorpusSample>[] = [
    {
      title: 'ID',
      width: 'w-16',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-3">{row.id}</span>,
    },
    {
      title: '模型',
      render: (row) => (
        <div className="flex flex-col">
          <span className="text-[13px] font-medium text-ink">{row.model}</span>
          {row.upstream_model && row.upstream_model !== row.model && (
            <span className="text-xs text-ink-3">上游 {row.upstream_model}</span>
          )}
        </div>
      ),
    },
    {
      title: '用户',
      render: (row) => <span className="text-[13px] text-ink-2">#{row.user_id}</span>,
    },
    {
      title: '状态',
      render: (row) => (
        <div className="flex flex-wrap items-center gap-1">
          <Badge tone={row.status_code >= 200 && row.status_code < 400 ? 'ok' : 'err'}>{row.status_code}</Badge>
          {row.is_stream && <Badge tone="info">流式</Badge>}
          {row.truncated && <Badge tone="warn">截断</Badge>}
          {row.incomplete && <Badge tone="err">不完整</Badge>}
        </div>
      ),
    },
    {
      title: '体积（↑请求 / ↓响应）',
      render: (row) => (
        <span className="whitespace-nowrap text-[13px] text-ink-3">
          ↑{formatBytes(row.request_bytes)} / ↓{formatBytes(row.response_bytes)}
        </span>
      ),
    },
    {
      title: '时间',
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
    },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <button type="button" className="text-[13px] text-ink-3 hover:text-brand" onClick={() => openDetail(row.id)}>
          查看全文
        </button>
      ),
    },
  ]

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-60">
          <Field label="模型">
            <Select value={modelDraft} onChange={(e) => setModelDraft(e.target.value)}>
              <option value="">全部模型</option>
              {models.map((m) => (
                <option key={m.model} value={m.model}>
                  {m.model}
                  {m.enabled ? '' : '（已停采）'}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <div className="w-44">
          <Field label="用户 ID">
            <Input
              value={userIdDraft}
              onChange={(e) => setUserIdDraft(e.target.value)}
              placeholder="可选"
              inputMode="numeric"
            />
          </Field>
        </div>
        <Button variant="secondary" onClick={handleSearch}>
          查询
        </Button>
        <Button variant="ghost" onClick={handleReset}>
          重置
        </Button>
        <div className="ml-auto">
          <Button variant="secondary" loading={exporting} onClick={() => setConfirmExport(true)}>
            导出 JSONL
          </Button>
        </div>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={items}
          rowKey={(row) => row.id}
          loading={loading}
          emptyTitle="没有符合条件的样本"
          emptyDescription="调整筛选条件，或确认清单中的模型已产生调用。"
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={SAMPLES_PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      {/* 全文弹层：唯一能读到对话原文的入口（后端每次都写审计日志） */}
      <Modal open={detailId !== null} onClose={() => setDetailId(null)} title={`样本 #${detailId ?? ''} 全文`} width={880}>
        {detailLoading || !detail ? (
          <SkeletonRows rows={4} />
        ) : (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[13px] text-ink-3">
              <span className="text-ink-2">{detail.model}</span>
              <span>用户 #{detail.user_id}</span>
              <span>渠道 #{detail.channel_id}</span>
              <span>{detail.is_stream ? '流式' : '非流式'}</span>
              <span>状态码 {detail.status_code}</span>
              <span>{formatDateTime(detail.created_at)}</span>
              {detail.truncated && <Badge tone="warn">入库时已截断</Badge>}
              {detail.incomplete && <Badge tone="err">响应不完整</Badge>}
            </div>
            <Field label="请求全文">
              <CodeBlock code={prettyJson(detail.request_body)} title="request" />
            </Field>
            <Field label="响应全文">
              <CodeBlock code={prettyJson(detail.response_body)} title="response" />
            </Field>
            <p className="text-xs text-ink-3">request_id: {detail.request_id} · 本次查看已写入审计日志</p>
          </div>
        )}
      </Modal>

      <ConfirmDialog
        open={confirmExport}
        title="导出语料样本"
        message="将按当前筛选条件导出样本全文（JSONL，一行一条）。导出内容包含用户对话原文，并会写入一条审计日志。确认继续？"
        confirmText="开始导出"
        onConfirm={handleExport}
        onCancel={() => setConfirmExport(false)}
      />
    </div>
  )
}

/* ── 分区三：统计总览 ───────────────────────────────────── */

function StatsSection({ models }: { models: CorpusModel[] }) {
  const { toastError } = useToast()

  const [stats, setStats] = useState<CorpusStats | null>(null)
  // 后端只提供全库汇总统计；「各语料的样本量」用每模型一条 size=1 的分页查询拿
  // total（清单通常只有个位数，请求量可控），避免为展示而加后端端点。
  const [perModel, setPerModel] = useState<{ model: string; enabled: boolean; samples: number }[] | null>(null)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const stat = await fetchCorpusStats()
      setStats(stat)
      const rows = await Promise.all(
        models.map(async (m) => {
          const paged = await listCorpusSamples({ model: m.model, page: 1, size: 1 })
          return { model: m.model, enabled: m.enabled, samples: paged.total }
        }),
      )
      setPerModel(rows)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '统计加载失败')
    } finally {
      setLoading(false)
    }
  }, [models, toastError])

  useEffect(() => {
    void load()
  }, [load])

  const perModelColumns: Column<{ model: string; enabled: boolean; samples: number }>[] = [
    {
      title: '模型',
      render: (row) => <span className="text-[13px] font-medium text-ink">{row.model}</span>,
    },
    {
      title: '采集状态',
      align: 'center',
      render: (row) => (row.enabled ? <Badge tone="ok">采集中</Badge> : <Badge tone="off">已停采</Badge>),
    },
    {
      title: '样本量',
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatNumber(row.samples)}</span>,
    },
  ]

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[13px] text-ink-3">全库汇总 + 按清单逐模型统计样本量</p>
        <Button variant="secondary" onClick={() => void load()} disabled={loading}>
          刷新
        </Button>
      </div>

      {stats && (
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <StatCard label="样本总量" value={formatNumber(stats.samples)} />
          <StatCard label="覆盖用户数" value={formatNumber(stats.users)} />
          <StatCard label="正在采集模型" value={formatNumber(stats.models)} hint={`清单共 ${models.length} 个`} />
          <StatCard label="免计费用户" value={formatNumber(stats.free_users)} hint="享有语料共建福利" />
          <StatCard label="请求体积" value={formatBytes(stats.request_bytes)} />
          <StatCard label="响应体积" value={formatBytes(stats.response_bytes)} />
          <StatCard label="最早样本" value={stats.earliest_at ? formatDateTime(stats.earliest_at) : '—'} />
          <StatCard label="最近样本" value={stats.latest_at ? formatDateTime(stats.latest_at) : '—'} />
        </div>
      )}

      <Card padding="none">
        <DataTable
          columns={perModelColumns}
          rows={perModel}
          rowKey={(row) => row.model}
          loading={loading}
          emptyTitle="清单为空"
          emptyDescription="先在「语料清单」里添加要采集的模型，这里才会出现分模型统计。"
        />
      </Card>
    </div>
  )
}

/* ── 分区四：福利授权 ───────────────────────────────────── */

function GrantsSection({ models }: { models: CorpusModel[] }) {
  const { toast, toastError } = useToast()

  const [items, setItems] = useState<CorpusGrant[] | null>(null)
  const [creating, setCreating] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<CorpusGrant | null>(null)
  const [revoking, setRevoking] = useState(false)

  const load = useCallback(async () => {
    try {
      const data = await listCorpusGrants()
      setItems(data.items)
    } catch (err) {
      setItems([])
      toastError(err instanceof Error ? err.message : '福利资格加载失败')
    }
  }, [toastError])

  useEffect(() => {
    void load()
  }, [load])

  async function handleRevoke() {
    if (!revokeTarget) return
    setRevoking(true)
    try {
      await deleteCorpusGrant(revokeTarget.user_id, revokeTarget.model)
      toast('福利资格已撤销')
      setRevokeTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '撤销失败')
    } finally {
      setRevoking(false)
    }
  }

  const columns: Column<CorpusGrant>[] = [
    {
      title: '用户',
      render: (row) => (
        <div className="flex flex-col">
          <span className="text-[13px] font-medium text-ink">{row.username || `#${row.user_id}`}</span>
          <span className="text-xs text-ink-3">{row.email || `ID ${row.user_id}`}</span>
        </div>
      ),
    },
    {
      title: '模型',
      render: (row) => <span className="text-[13px] text-ink">{row.model}</span>,
    },
    {
      title: '免计费',
      align: 'center',
      render: (row) => (row.free_access ? <Badge tone="ok">免计费</Badge> : <Badge tone="off">计费</Badge>),
    },
    {
      title: '状态',
      align: 'center',
      render: (row) => (row.active ? <Badge tone="ok">生效中</Badge> : <Badge tone="off">已失效</Badge>),
    },
    {
      title: '备注',
      render: (row) => (
        <span className="block max-w-48 truncate text-[13px] text-ink-3" title={row.remark}>
          {row.remark || '—'}
        </span>
      ),
    },
    {
      title: '更新时间',
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.updated_at)}</span>,
    },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <button type="button" className="text-[13px] text-ink-3 hover:text-err" onClick={() => setRevokeTarget(row)}>
          撤销
        </button>
      ),
    },
  ]

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[13px] text-ink-3">
          被授权用户调用对应模型时不计费（以对话贡献换取福利）· 撤销立即生效
        </p>
        <Button variant="primary" onClick={() => setCreating(true)}>
          发放资格
        </Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={items}
          rowKey={(row) => `${row.user_id}:${row.model}`}
          loading={items === null}
          emptyTitle="还没有福利资格"
          emptyDescription="发放资格后，对应用户调用该模型的请求将不计费。"
        />
      </Card>

      {creating && (
        <GrantUpsertModal
          models={models}
          onClose={() => setCreating(false)}
          onSaved={() => {
            setCreating(false)
            void load()
          }}
        />
      )}

      <ConfirmDialog
        open={Boolean(revokeTarget)}
        title="撤销福利资格"
        message={`确认撤销「${revokeTarget?.username || `#${revokeTarget?.user_id}`}」对「${revokeTarget?.model}」的福利资格？撤销立即生效，该用户后续调用将正常计费。`}
        danger
        confirmText="撤销"
        loading={revoking}
        onConfirm={handleRevoke}
        onCancel={() => setRevokeTarget(null)}
      />
    </div>
  )
}

/** 福利资格发放弹层（发放语义 = upsert：同一用户对同一模型重复发放即覆盖续授） */
function GrantUpsertModal({
  models,
  onClose,
  onSaved,
}: {
  models: CorpusModel[]
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const [email, setEmail] = useState('')
  const [userIdText, setUserIdText] = useState('')
  const [model, setModel] = useState(models[0]?.model ?? '')
  const [freeAccess, setFreeAccess] = useState(true)
  const [remark, setRemark] = useState('')
  const [loading, setLoading] = useState(false)

  async function handleSubmit() {
    const trimmedEmail = email.trim()
    const trimmedId = userIdText.trim()
    // 邮箱优先：后端按「邮箱能定位就按邮箱」处理，因此两者都填时只送邮箱
    if (!trimmedEmail && !trimmedId) {
      toastError('请填写邮箱或用户 ID（二选一）')
      return
    }
    if (trimmedId && !/^\d+$/.test(trimmedId)) {
      toastError('用户 ID 必须是纯数字')
      return
    }
    if (!model) {
      toastError('请选择要授权的模型')
      return
    }
    setLoading(true)
    try {
      await upsertCorpusGrant({
        email: trimmedEmail || undefined,
        user_id: trimmedEmail ? undefined : Number(trimmedId),
        model,
        free_access: freeAccess,
        remark: remark.trim() || undefined,
      })
      toast('福利资格已发放')
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '发放失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="发放福利资格" width={560}>
      <div className="space-y-4">
        <Field label="用户邮箱" required help="优先按邮箱定位用户；邮箱查无此人会被后端直接拒绝，避免抄错 ID">
          <Input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="user@example.com" />
        </Field>
        <Field label="或用户 ID" help="仅在不用邮箱时填写；两者都填时以邮箱为准">
          <Input
            value={userIdText}
            onChange={(e) => setUserIdText(e.target.value)}
            placeholder="可选"
            inputMode="numeric"
            disabled={email.trim() !== ''}
          />
        </Field>
        <Field label="授权模型" required help={models.length ? undefined : '清单为空：请先在「语料清单」添加模型'}>
          <Select value={model} onChange={(e) => setModel(e.target.value)} disabled={models.length === 0}>
            {models.map((m) => (
              <option key={m.model} value={m.model}>
                {m.model}
              </option>
            ))}
          </Select>
        </Field>
        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>免计费</span>
          <Switch checked={freeAccess} onChange={setFreeAccess} label="免计费" />
        </label>
        <Field label="备注">
          <Textarea value={remark} onChange={(e) => setRemark(e.target.value)} rows={2} placeholder="可选，如「共建计划首批参与者」" />
        </Field>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          取消
        </Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit} disabled={models.length === 0}>
          发放
        </Button>
      </div>
    </Modal>
  )
}
