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
import { useI18n } from '@/i18n'
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
  const { t } = useI18n()

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
      setModelsError(err instanceof Error ? err.message : t('admin.corpus.loadFailed'))
    }
  }, [t])

  useEffect(() => {
    void reloadModels()
  }, [reloadModels])

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">{t('admin.corpus.title')}</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">
          {t('admin.corpus.subtitle')}
        </p>
      </div>

      {modelsError && (
        <Card className="border-err/30">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-[13px] text-err">{modelsError}</p>
            <Button variant="secondary" size="sm" onClick={() => void reloadModels()}>
              {t('admin.corpus.retry')}
            </Button>
          </div>
        </Card>
      )}

      <Tabs<TabKey>
        items={[
          { value: 'models', label: t('admin.corpus.tab.models'), count: models?.length },
          { value: 'samples', label: t('admin.corpus.tab.samples') },
          { value: 'stats', label: t('admin.corpus.tab.stats') },
          { value: 'grants', label: t('admin.corpus.tab.grants') },
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
  const { t } = useI18n()

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
      toast(row.enabled ? t('admin.corpus.models.toast.paused', { model: row.model }) : t('admin.corpus.models.toast.started', { model: row.model }))
      onChanged()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.corpus.models.toast.toggleFailed'))
    } finally {
      setSwitching('')
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteCorpusModel(deleteTarget.model)
      toast(t('admin.corpus.models.toast.removed'))
      setDeleteTarget(null)
      onChanged()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.corpus.models.toast.removeFailed'))
    }
  }

  const columns: Column<CorpusModel>[] = [
    {
      title: t('admin.corpus.models.col.model'),
      render: (row) => <span className="text-[13px] font-medium text-ink">{row.model}</span>,
    },
    {
      title: t('admin.corpus.models.col.collecting'),
      align: 'center',
      render: (row) => (
        <div className="flex justify-center">
          <Switch
            checked={row.enabled}
            disabled={switching === row.model}
            onChange={() => handleToggle(row)}
            label={t('admin.corpus.models.switchLabel', { model: row.model })}
          />
        </div>
      ),
    },
    {
      title: t('admin.corpus.models.col.remark'),
      render: (row) => (
        <span className="block max-w-64 truncate text-[13px] text-ink-3" title={row.remark}>
          {row.remark || '—'}
        </span>
      ),
    },
    {
      title: t('admin.corpus.models.col.updatedAt'),
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.updated_at)}</span>,
    },
    {
      title: t('admin.corpus.models.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-3 text-[13px]">
          <button type="button" className="text-ink-3 hover:text-brand" onClick={() => setEditing(row)}>
            {t('admin.corpus.models.action.edit')}
          </button>
          <button type="button" className="text-ink-3 hover:text-err" onClick={() => setDeleteTarget(row)}>
            {t('admin.corpus.models.action.remove')}
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[13px] text-ink-3">{t('admin.corpus.models.summary', { count: models?.length ?? 0 })}</p>
        <Button variant="primary" onClick={() => setEditing('new')}>
          {t('admin.corpus.models.create')}
        </Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={models}
          rowKey={(row) => row.model}
          loading={models === null}
          emptyTitle={t('admin.corpus.models.emptyTitle')}
          emptyDescription={t('admin.corpus.models.emptyDescription')}
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
        title={t('admin.corpus.models.confirm.removeTitle')}
        message={t('admin.corpus.models.confirm.removeMessage', { model: deleteTarget?.model ?? '' })}
        danger
        confirmText={t('admin.corpus.models.confirm.remove')}
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
  const { t } = useI18n()
  const isEdit = target !== 'new'
  // 弹层只在打开时挂载，初始值直接取自 target，无需 useEffect 复位
  const [model, setModel] = useState(isEdit ? target.model : '')
  const [enabled, setEnabled] = useState(isEdit ? target.enabled : true)
  const [remark, setRemark] = useState(isEdit ? target.remark : '')
  const [loading, setLoading] = useState(false)

  async function handleSubmit() {
    const name = model.trim()
    if (!name) {
      toastError(t('admin.corpus.models.error.modelRequired'))
      return
    }
    setLoading(true)
    try {
      await upsertCorpusModel({ model: name, enabled, remark: remark.trim() })
      toast(isEdit ? t('admin.corpus.models.toast.updated') : t('admin.corpus.models.toast.created'))
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.corpus.models.toast.saveFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={isEdit ? t('admin.corpus.models.form.editTitle', { model: target.model }) : t('admin.corpus.models.form.newTitle')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.corpus.models.form.model')} required help={t('admin.corpus.models.form.modelHelp')}>
          <Input
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder={t('admin.corpus.models.form.modelPlaceholder')}
            disabled={isEdit}
          />
        </Field>
        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>{t('admin.corpus.models.form.enabled')}</span>
          <Switch checked={enabled} onChange={setEnabled} label={t('admin.corpus.models.form.enabled')} />
        </label>
        <Field label={t('admin.corpus.models.form.remark')}>
          <Textarea value={remark} onChange={(e) => setRemark(e.target.value)} rows={2} placeholder={t('admin.corpus.models.form.remarkPlaceholder')} />
        </Field>
        {isEdit && (
          <p className="text-xs text-ink-3">{t('admin.corpus.models.form.overwriteHint')}</p>
        )}
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('common.action.cancel')}
        </Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>
          {t('common.action.save')}
        </Button>
      </div>
    </Modal>
  )
}

/* ── 分区二：样本浏览 ───────────────────────────────────── */

function SamplesSection({ models }: { models: CorpusModel[] }) {
  const { toast, toastError } = useToast()
  const { t } = useI18n()

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
      toastError(err instanceof Error ? err.message : t('admin.corpus.samples.toast.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [filter, page, toastError, t])

  useEffect(() => {
    void load()
  }, [load])

  function handleSearch() {
    const trimmed = userIdDraft.trim()
    if (trimmed && !/^\d+$/.test(trimmed)) {
      toastError(t('admin.corpus.samples.error.userIdNumeric'))
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
      toastError(err instanceof Error ? err.message : t('admin.corpus.samples.toast.detailFailed'))
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
      toast(t('admin.corpus.samples.toast.exportStarted'))
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.corpus.samples.toast.exportFailed'))
    } finally {
      setExporting(false)
    }
  }

  const columns: Column<CorpusSample>[] = [
    {
      title: t('admin.corpus.samples.col.id'),
      width: 'w-16',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-3">{row.id}</span>,
    },
    {
      title: t('admin.corpus.samples.col.model'),
      render: (row) => (
        <div className="flex flex-col">
          <span className="text-[13px] font-medium text-ink">{row.model}</span>
          {row.upstream_model && row.upstream_model !== row.model && (
            <span className="text-xs text-ink-3">{t('admin.corpus.samples.upstream', { model: row.upstream_model })}</span>
          )}
        </div>
      ),
    },
    {
      title: t('admin.corpus.samples.col.user'),
      render: (row) => <span className="text-[13px] text-ink-2">#{row.user_id}</span>,
    },
    {
      title: t('admin.corpus.samples.col.status'),
      render: (row) => (
        <div className="flex flex-wrap items-center gap-1">
          <Badge tone={row.status_code >= 200 && row.status_code < 400 ? 'ok' : 'err'}>{row.status_code}</Badge>
          {row.is_stream && <Badge tone="info">{t('admin.corpus.samples.badgeStream')}</Badge>}
          {row.truncated && <Badge tone="warn">{t('admin.corpus.samples.badgeTruncated')}</Badge>}
          {row.incomplete && <Badge tone="err">{t('admin.corpus.samples.badgeIncomplete')}</Badge>}
        </div>
      ),
    },
    {
      title: t('admin.corpus.samples.col.size'),
      render: (row) => (
        <span className="whitespace-nowrap text-[13px] text-ink-3">
          ↑{formatBytes(row.request_bytes)} / ↓{formatBytes(row.response_bytes)}
        </span>
      ),
    },
    {
      title: t('admin.corpus.samples.col.time'),
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
    },
    {
      title: t('admin.corpus.samples.col.actions'),
      align: 'right',
      render: (row) => (
        <button type="button" className="text-[13px] text-ink-3 hover:text-brand" onClick={() => openDetail(row.id)}>
          {t('admin.corpus.samples.viewFull')}
        </button>
      ),
    },
  ]

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-60">
          <Field label={t('admin.corpus.samples.filterModel')}>
            <Select value={modelDraft} onChange={(e) => setModelDraft(e.target.value)}>
              <option value="">{t('admin.corpus.samples.allModels')}</option>
              {models.map((m) => (
                <option key={m.model} value={m.model}>
                  {m.model}
                  {m.enabled ? '' : t('admin.corpus.samples.stoppedSuffix')}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <div className="w-44">
          <Field label={t('admin.corpus.samples.filterUserId')}>
            <Input
              value={userIdDraft}
              onChange={(e) => setUserIdDraft(e.target.value)}
              placeholder={t('admin.corpus.samples.optional')}
              inputMode="numeric"
            />
          </Field>
        </div>
        <Button variant="secondary" onClick={handleSearch}>
          {t('admin.corpus.samples.search')}
        </Button>
        <Button variant="ghost" onClick={handleReset}>
          {t('admin.corpus.samples.reset')}
        </Button>
        <div className="ml-auto">
          <Button variant="secondary" loading={exporting} onClick={() => setConfirmExport(true)}>
            {t('admin.corpus.samples.export')}
          </Button>
        </div>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={items}
          rowKey={(row) => row.id}
          loading={loading}
          emptyTitle={t('admin.corpus.samples.emptyTitle')}
          emptyDescription={t('admin.corpus.samples.emptyDescription')}
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={SAMPLES_PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      {/* 全文弹层：唯一能读到对话原文的入口（后端每次都写审计日志） */}
      <Modal open={detailId !== null} onClose={() => setDetailId(null)} title={t('admin.corpus.samples.detailTitle', { id: detailId ?? '' })} width={880}>
        {detailLoading || !detail ? (
          <SkeletonRows rows={4} />
        ) : (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[13px] text-ink-3">
              <span className="text-ink-2">{detail.model}</span>
              <span>{t('admin.corpus.samples.detail.user', { id: detail.user_id })}</span>
              <span>{t('admin.corpus.samples.detail.channel', { id: detail.channel_id })}</span>
              <span>{detail.is_stream ? t('admin.corpus.samples.detail.stream') : t('admin.corpus.samples.detail.nonStream')}</span>
              <span>{t('admin.corpus.samples.detail.statusCode', { code: detail.status_code })}</span>
              <span>{formatDateTime(detail.created_at)}</span>
              {detail.truncated && <Badge tone="warn">{t('admin.corpus.samples.detail.truncated')}</Badge>}
              {detail.incomplete && <Badge tone="err">{t('admin.corpus.samples.detail.incomplete')}</Badge>}
            </div>
            <Field label={t('admin.corpus.samples.detail.requestFull')}>
              <CodeBlock code={prettyJson(detail.request_body)} title="request" />
            </Field>
            <Field label={t('admin.corpus.samples.detail.responseFull')}>
              <CodeBlock code={prettyJson(detail.response_body)} title="response" />
            </Field>
            <p className="text-xs text-ink-3">{t('admin.corpus.samples.detail.auditHint', { id: detail.request_id })}</p>
          </div>
        )}
      </Modal>

      <ConfirmDialog
        open={confirmExport}
        title={t('admin.corpus.samples.exportConfirm.title')}
        message={t('admin.corpus.samples.exportConfirm.message')}
        confirmText={t('admin.corpus.samples.exportConfirm.confirmText')}
        onConfirm={handleExport}
        onCancel={() => setConfirmExport(false)}
      />
    </div>
  )
}

/* ── 分区三：统计总览 ───────────────────────────────────── */

function StatsSection({ models }: { models: CorpusModel[] }) {
  const { toastError } = useToast()
  const { t } = useI18n()

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
      toastError(err instanceof Error ? err.message : t('admin.corpus.stats.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [models, toastError, t])

  useEffect(() => {
    void load()
  }, [load])

  const perModelColumns: Column<{ model: string; enabled: boolean; samples: number }>[] = [
    {
      title: t('admin.corpus.stats.col.model'),
      render: (row) => <span className="text-[13px] font-medium text-ink">{row.model}</span>,
    },
    {
      title: t('admin.corpus.stats.col.status'),
      align: 'center',
      render: (row) => (row.enabled ? <Badge tone="ok">{t('admin.corpus.stats.collecting')}</Badge> : <Badge tone="off">{t('admin.corpus.stats.stopped')}</Badge>),
    },
    {
      title: t('admin.corpus.stats.col.sampleCount'),
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatNumber(row.samples)}</span>,
    },
  ]

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[13px] text-ink-3">{t('admin.corpus.stats.summary')}</p>
        <Button variant="secondary" onClick={() => void load()} disabled={loading}>
          {t('admin.corpus.stats.refresh')}
        </Button>
      </div>

      {stats && (
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <StatCard label={t('admin.corpus.stats.samples')} value={formatNumber(stats.samples)} />
          <StatCard label={t('admin.corpus.stats.users')} value={formatNumber(stats.users)} />
          <StatCard label={t('admin.corpus.stats.models')} value={formatNumber(stats.models)} hint={t('admin.corpus.stats.modelsHint', { count: models.length })} />
          <StatCard label={t('admin.corpus.stats.freeUsers')} value={formatNumber(stats.free_users)} hint={t('admin.corpus.stats.freeUsersHint')} />
          <StatCard label={t('admin.corpus.stats.requestBytes')} value={formatBytes(stats.request_bytes)} />
          <StatCard label={t('admin.corpus.stats.responseBytes')} value={formatBytes(stats.response_bytes)} />
          <StatCard label={t('admin.corpus.stats.earliest')} value={stats.earliest_at ? formatDateTime(stats.earliest_at) : '—'} />
          <StatCard label={t('admin.corpus.stats.latest')} value={stats.latest_at ? formatDateTime(stats.latest_at) : '—'} />
        </div>
      )}

      <Card padding="none">
        <DataTable
          columns={perModelColumns}
          rows={perModel}
          rowKey={(row) => row.model}
          loading={loading}
          emptyTitle={t('admin.corpus.stats.emptyTitle')}
          emptyDescription={t('admin.corpus.stats.emptyDescription')}
        />
      </Card>
    </div>
  )
}

/* ── 分区四：福利授权 ───────────────────────────────────── */

function GrantsSection({ models }: { models: CorpusModel[] }) {
  const { toast, toastError } = useToast()
  const { t } = useI18n()

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
      toastError(err instanceof Error ? err.message : t('admin.corpus.grants.loadFailed'))
    }
  }, [toastError, t])

  useEffect(() => {
    void load()
  }, [load])

  async function handleRevoke() {
    if (!revokeTarget) return
    setRevoking(true)
    try {
      await deleteCorpusGrant(revokeTarget.user_id, revokeTarget.model)
      toast(t('admin.corpus.grants.toast.revoked'))
      setRevokeTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.corpus.grants.toast.revokeFailed'))
    } finally {
      setRevoking(false)
    }
  }

  const columns: Column<CorpusGrant>[] = [
    {
      title: t('admin.corpus.grants.col.user'),
      render: (row) => (
        <div className="flex flex-col">
          <span className="text-[13px] font-medium text-ink">{row.username || `#${row.user_id}`}</span>
          <span className="text-xs text-ink-3">{row.email || `ID ${row.user_id}`}</span>
        </div>
      ),
    },
    {
      title: t('admin.corpus.grants.col.model'),
      render: (row) => <span className="text-[13px] text-ink">{row.model}</span>,
    },
    {
      title: t('admin.corpus.grants.col.freeAccess'),
      align: 'center',
      render: (row) => (row.free_access ? <Badge tone="ok">{t('admin.corpus.grants.freeAccess')}</Badge> : <Badge tone="off">{t('admin.corpus.grants.billed')}</Badge>),
    },
    {
      title: t('admin.corpus.grants.col.status'),
      align: 'center',
      render: (row) => (row.active ? <Badge tone="ok">{t('admin.corpus.grants.active')}</Badge> : <Badge tone="off">{t('admin.corpus.grants.inactive')}</Badge>),
    },
    {
      title: t('admin.corpus.grants.col.remark'),
      render: (row) => (
        <span className="block max-w-48 truncate text-[13px] text-ink-3" title={row.remark}>
          {row.remark || '—'}
        </span>
      ),
    },
    {
      title: t('admin.corpus.grants.col.updatedAt'),
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.updated_at)}</span>,
    },
    {
      title: t('admin.corpus.grants.col.actions'),
      align: 'right',
      render: (row) => (
        <button type="button" className="text-[13px] text-ink-3 hover:text-err" onClick={() => setRevokeTarget(row)}>
          {t('admin.corpus.grants.revoke')}
        </button>
      ),
    },
  ]

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[13px] text-ink-3">
          {t('admin.corpus.grants.summary')}
        </p>
        <Button variant="primary" onClick={() => setCreating(true)}>
          {t('admin.corpus.grants.create')}
        </Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={items}
          rowKey={(row) => `${row.user_id}:${row.model}`}
          loading={items === null}
          emptyTitle={t('admin.corpus.grants.emptyTitle')}
          emptyDescription={t('admin.corpus.grants.emptyDescription')}
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
        title={t('admin.corpus.grants.confirm.title')}
        message={t('admin.corpus.grants.confirm.message', { user: revokeTarget?.username || `#${revokeTarget?.user_id}`, model: revokeTarget?.model ?? '' })}
        danger
        confirmText={t('admin.corpus.grants.confirm.confirmText')}
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
  const { t } = useI18n()
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
      toastError(t('admin.corpus.grants.error.emailOrIdRequired'))
      return
    }
    if (trimmedId && !/^\d+$/.test(trimmedId)) {
      toastError(t('admin.corpus.grants.error.userIdNumeric'))
      return
    }
    if (!model) {
      toastError(t('admin.corpus.grants.error.modelRequired'))
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
      toast(t('admin.corpus.grants.toast.granted'))
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.corpus.grants.toast.grantFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={t('admin.corpus.grants.form.title')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.corpus.grants.form.email')} required help={t('admin.corpus.grants.form.emailHelp')}>
          <Input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="user@example.com" />
        </Field>
        <Field label={t('admin.corpus.grants.form.id')} help={t('admin.corpus.grants.form.idHelp')}>
          <Input
            value={userIdText}
            onChange={(e) => setUserIdText(e.target.value)}
            placeholder={t('admin.corpus.grants.form.optional')}
            inputMode="numeric"
            disabled={email.trim() !== ''}
          />
        </Field>
        <Field label={t('admin.corpus.grants.form.model')} required help={models.length ? undefined : t('admin.corpus.grants.form.modelHelpEmpty')}>
          <Select value={model} onChange={(e) => setModel(e.target.value)} disabled={models.length === 0}>
            {models.map((m) => (
              <option key={m.model} value={m.model}>
                {m.model}
              </option>
            ))}
          </Select>
        </Field>
        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>{t('admin.corpus.grants.form.freeAccess')}</span>
          <Switch checked={freeAccess} onChange={setFreeAccess} label={t('admin.corpus.grants.form.freeAccess')} />
        </label>
        <Field label={t('admin.corpus.grants.form.remark')}>
          <Textarea value={remark} onChange={(e) => setRemark(e.target.value)} rows={2} placeholder={t('admin.corpus.grants.form.remarkPlaceholder')} />
        </Field>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('admin.corpus.grants.form.cancel')}
        </Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit} disabled={models.length === 0}>
          {t('admin.corpus.grants.form.submit')}
        </Button>
      </div>
    </Modal>
  )
}
