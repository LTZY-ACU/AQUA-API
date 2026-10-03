/** 管理后台：运维监控与备份（/admin/maintenance）。
 *
 * 意图（Why）：
 *   运维页回答三个问题：「现在健不健康」「怎么留一份数据」「这份备份能不能用」。
 *   顶部 StatCard 组给出版本 / 运行时长 / 数据库体积 / 磁盘 / 调用健康度，
 *   下方两张卡分别展示数据库各表行数与备份（下载 + 上传只读校验）。
 *
 * 流转（Flow）：
 *   本页 → fetchMaintenanceOverview() → GET /api/admin/maintenance/overview
 *   下载 → downloadMaintenanceBackup()（内部用 fetch 附带令牌触发浏览器保存）
 *   校验 → inspectMaintenanceBackup(file) → POST /api/admin/maintenance/backup/inspect
 *
 * 扩展（Extend）：
 *   新增运维指标：在 MaintenanceOverview 加字段 + 补一张 StatCard；
 *   后端刻意不提供在线恢复，只给 restore_steps 人工步骤，前端原样展示。
 */
'use client'

import { useCallback, useEffect, useRef, useState, type ChangeEvent } from 'react'

import {
  downloadMaintenanceBackup,
  fetchMaintenanceOverview,
  inspectMaintenanceBackup,
  type MaintenanceCompareRow,
  type MaintenanceInspectResult,
  type MaintenanceOverview,
  type MaintenanceRetryRatio,
} from '@/api/maintenance'
import { Badge, Card, Skeleton, SkeletonRows, StatCard } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { DataTable, type Column } from '@/components/ui/Table'
import { translate, useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime, formatNumber, EMPTY } from '@/utils/format'

/** 秒 → 「X 天 Y 小时 / X 小时 Y 分 / Y 分钟」的可读时长（<1 分钟显示秒数） */
function formatUptime(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return EMPTY
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const mins = Math.floor((seconds % 3600) / 60)
  if (days > 0) return translate('admin.maintenance.uptimeDays', { days, hours })
  if (hours > 0) return translate('admin.maintenance.uptimeHours', { hours, mins })
  if (mins > 0) return translate('admin.maintenance.uptimeMins', { mins })
  return translate('admin.maintenance.uptimeSecs', { seconds })
}

/** 字节 → MB 字符串（保留 1 位小数） */
function formatMB(bytes: number | undefined): string {
  if (!bytes || bytes <= 0) return EMPTY
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

export default function AdminMaintenancePage() {
  const { toast, toastError } = useToast()
  const { t } = useI18n()

  const [overview, setOverview] = useState<MaintenanceOverview | null>(null)
  const [loading, setLoading] = useState(true)

  // 备份下载 / 上传校验
  const [downloading, setDownloading] = useState(false)
  const [inspecting, setInspecting] = useState(false)
  const [inspect, setInspect] = useState<MaintenanceInspectResult | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    void fetchMaintenanceOverview()
      .then(setOverview)
      .catch((err) => toastError(err instanceof Error ? err.message : t('admin.maintenance.toast.overviewFailed')))
      .finally(() => setLoading(false))
  }, [toastError, t])

  /** 下载数据库一致性快照（浏览器侧触发保存） */
  async function handleDownload() {
    setDownloading(true)
    try {
      await downloadMaintenanceBackup()
      toast(t('admin.maintenance.toast.downloadStarted'))
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.maintenance.toast.downloadFailed'))
    } finally {
      setDownloading(false)
    }
  }

  /** 选择文件 → 上传只读校验；允许重复选同一文件（先清空 input.value） */
  async function handleFile(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    setInspecting(true)
    setInspect(null)
    try {
      const result = await inspectMaintenanceBackup(file)
      setInspect(result)
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.maintenance.toast.inspectFailed'))
    } finally {
      setInspecting(false)
    }
  }

  /** 折扣分组重试率的列定义 */
  const retryRatioColumns: Column<MaintenanceRetryRatio>[] = [
    { title: t('admin.maintenance.retryCol.group'), render: (row) => <span className="font-medium text-ink">{row.group}</span> },
    { title: t('admin.maintenance.retryCol.ratio'), align: 'right', render: (row) => <span className="tabular-nums text-ink-2">{row.ratio}%</span> },
    { title: t('admin.maintenance.retryCol.upstreamCalls'), align: 'right', render: (row) => <span className="tabular-nums text-ink-2">{formatNumber(row.upstream_calls)}</span> },
    { title: t('admin.maintenance.retryCol.chargedRequests'), align: 'right', render: (row) => <span className="tabular-nums text-ink-2">{formatNumber(row.charged_requests)}</span> },
    {
      title: t('admin.maintenance.retryCol.retryRatio'),
      align: 'right',
      render: (row) => (
        <span className={`tabular-nums font-medium ${row.over_break_even ? 'text-err' : 'text-ink-2'}`}>
          {row.retry_ratio.toFixed(3)}
        </span>
      ),
    },
    {
      title: t('admin.maintenance.retryCol.status'),
      align: 'center',
      render: (row) =>
        row.over_break_even ? <Badge tone="err">{t('admin.maintenance.losing')}</Badge> : <Badge tone="ok">{t('admin.maintenance.normal')}</Badge>,
    },
  ]

  const usage24h = overview?.usage.last_24h
  const usage7d = overview?.usage.last_7d

  const tableColumns: Column<MaintenanceOverview['tables'][number]>[] = [
    { title: t('admin.maintenance.tablesCol.name'), render: (row) => <span className="text-[13px] font-medium text-ink">{row.name}</span> },
    {
      title: t('admin.maintenance.tablesCol.rows'),
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatNumber(row.rows)}</span>,
    },
  ]

  const compareColumns: Column<MaintenanceCompareRow>[] = [
    { title: t('admin.maintenance.compareCol.name'), render: (row) => <span className="text-[13px] font-medium text-ink">{row.name}</span> },
    {
      title: t('admin.maintenance.compareCol.inBackup'),
      align: 'center',
      render: (row) => (row.in_backup ? <Badge tone="ok">{t('admin.maintenance.exists')}</Badge> : <Badge tone="err">{t('admin.maintenance.missing')}</Badge>),
    },
    {
      title: t('admin.maintenance.compareCol.backupRows'),
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{row.in_backup ? formatNumber(row.backup_rows) : '—'}</span>,
    },
    {
      title: t('admin.maintenance.compareCol.currentRows'),
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatNumber(row.current_rows)}</span>,
    },
    {
      title: t('admin.maintenance.compareCol.diff'),
      align: 'center',
      render: (row) =>
        row.in_backup && row.backup_rows !== row.current_rows ? (
          <Badge tone="warn">{t('admin.maintenance.mismatch')}</Badge>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
  ]

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">{t('admin.maintenance.title')}</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.maintenance.subtitle')}</p>
      </div>

      {/* 顶部指标卡 */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label={t('admin.maintenance.stat.version')} value={overview ? overview.version : '—'} hint={overview?.build_time ? t('admin.maintenance.stat.buildHint', { time: overview.build_time }) : undefined} />
        <StatCard
          label={t('admin.maintenance.stat.startedAt')}
          value={overview ? formatDateTime(overview.started_at) : '—'}
          hint={overview ? t('admin.maintenance.stat.uptimeHint', { uptime: formatUptime(overview.uptime_seconds) }) : undefined}
        />
        <StatCard
          label={t('admin.maintenance.stat.gitCommit')}
          value={overview && overview.git_commit ? overview.git_commit.slice(0, 8) : '—'}
          hint={overview?.git_commit || undefined}
        />
        <StatCard
          label={t('admin.maintenance.stat.databaseSize')}
          value={overview ? (overview.database.size_available ? formatMB(overview.database.size_bytes) : '—') : '—'}
          hint={overview ? t('admin.maintenance.stat.driverHint', { driver: overview.database.driver }) : undefined}
        />
        <StatCard
          label={t('admin.maintenance.stat.diskUsage')}
          value={overview && overview.disk.available ? `${(overview.disk.used_ratio * 100).toFixed(1)}%` : '—'}
          hint={
            overview && overview.disk.available
              ? t('admin.maintenance.stat.diskHint', { free: formatMB(overview.disk.free_bytes), total: formatMB(overview.disk.total_bytes) })
              : undefined
          }
        />
        <StatCard
          label={t('admin.maintenance.stat.requests24h')}
          value={usage24h ? formatNumber(usage24h.requests) : '—'}
          hint={usage24h ? t('admin.maintenance.stat.failuresHint', { count: formatNumber(usage24h.failures), rate: (usage24h.failure_rate * 100).toFixed(1) }) : undefined}
        />
        <StatCard
          label={t('admin.maintenance.stat.requests7d')}
          value={usage7d ? formatNumber(usage7d.requests) : '—'}
          hint={usage7d ? t('admin.maintenance.stat.failuresHint', { count: formatNumber(usage7d.failures), rate: (usage7d.failure_rate * 100).toFixed(1) }) : undefined}
        />
        <StatCard
          label={t('admin.maintenance.stat.avgLatency')}
          value={usage24h && usage24h.avg_latency_ms ? `${usage24h.avg_latency_ms} ms` : '—'}
          hint={usage7d && usage7d.avg_latency_ms ? t('admin.maintenance.stat.avgLatency7d', { ms: usage7d.avg_latency_ms }) : undefined}
        />
      </div>

      {/* 折扣分组重试率：r = 上游调用次数 / 计费请求次数。
          6 折档的净利本就薄，r 越过保本线（1.37）即正在亏本——必须让它可见。 */}
      {overview && overview.retry_ratios.length > 0 && (
        <Card padding="none">
          <div className="flex items-center justify-between border-b border-line px-4 py-3">
            <h2 className="text-sm font-semibold text-ink">{t('admin.maintenance.retryTitle')}</h2>
            <span className="text-xs text-ink-3">{t('admin.maintenance.retryHint')}</span>
          </div>
          <DataTable
            columns={retryRatioColumns}
            rows={overview.retry_ratios}
            loading={false}
            rowKey={(row) => row.group}
            emptyTitle={t('admin.maintenance.retryEmpty')}
          />
        </Card>
      )}

      {/* 数据库表行数 */}
      <Card padding="none">
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <h2 className="text-sm font-semibold text-ink">{t('admin.maintenance.tablesTitle')}</h2>
        </div>
        <DataTable
          columns={tableColumns}
          rows={loading ? null : (overview?.tables ?? [])}
          loading={loading}
          rowKey={(row) => row.name}
          emptyTitle={t('admin.maintenance.tablesEmpty')}
        />
      </Card>

      {/* 备份管理 */}
      <Card padding="none">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3">
          <h2 className="text-sm font-semibold text-ink">{t('admin.maintenance.backupTitle')}</h2>
          <div className="flex items-center gap-2">
            <input ref={fileRef} type="file" accept=".db,.sqlite,.sqlite3,.bak" className="hidden" onChange={handleFile} />
            <Button variant="secondary" loading={inspecting} onClick={() => fileRef.current?.click()}>
              {t('admin.maintenance.uploadBackup')}
            </Button>
            <Button variant="primary" loading={downloading} onClick={handleDownload}>
              {t('admin.maintenance.downloadBackup')}
            </Button>
          </div>
        </div>

        <div className="p-4">
          {inspecting && !inspect ? (
            <SkeletonRows rows={2} />
          ) : inspect ? (
            <div className="space-y-4">
              <div className="flex flex-wrap items-center gap-3 text-[13px]">
                {inspect.valid ? <Badge tone="ok">{t('admin.maintenance.inspectValid')}</Badge> : <Badge tone="err">{t('admin.maintenance.inspectInvalid')}</Badge>}
                <span className="text-ink-2">
                  {t('admin.maintenance.inspectSchema', { schema: inspect.schema_version, current: inspect.current_schema_version })}
                </span>
                <span className="text-ink-3">
                  {t('admin.maintenance.inspectTablePresent', { value: inspect.schema_table_present ? t('admin.maintenance.yes') : t('admin.maintenance.no') })}
                </span>
              </div>
              {inspect.restore_steps.length > 0 && (
                <div className="rounded-md border border-line bg-surface p-3">
                  <div className="mb-1.5 text-xs font-medium text-ink-3">{t('admin.maintenance.restoreTitle')}</div>
                  <ol className="list-decimal space-y-1 pl-5 text-[13px] text-ink-2">
                    {inspect.restore_steps.map((step, i) => (
                      <li key={i}>{step}</li>
                    ))}
                  </ol>
                  {inspect.note && <div className="mt-1.5 text-xs text-ink-3">{inspect.note}</div>}
                </div>
              )}
              <DataTable
                columns={compareColumns}
                rows={inspect.tables}
                rowKey={(row) => row.name}
                emptyTitle={t('admin.maintenance.compareEmpty')}
              />
            </div>
          ) : (
            <div className="text-[13px] text-ink-3">
              {t('admin.maintenance.backupHint')}
            </div>
          )}
        </div>
      </Card>

      {loading && !overview && <Skeleton className="h-40 w-full" />}
    </div>
  )
}