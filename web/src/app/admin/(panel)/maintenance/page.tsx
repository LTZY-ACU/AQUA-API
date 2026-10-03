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
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime, formatNumber, EMPTY } from '@/utils/format'

/** 秒 → 「X 天 Y 小时 / X 小时 Y 分 / Y 分钟」的可读时长（<1 分钟显示秒数） */
function formatUptime(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return EMPTY
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const mins = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时 ${mins} 分`
  if (mins > 0) return `${mins} 分钟`
  return `${seconds} 秒`
}

/** 字节 → MB 字符串（保留 1 位小数） */
function formatMB(bytes: number | undefined): string {
  if (!bytes || bytes <= 0) return EMPTY
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

export default function AdminMaintenancePage() {
  const { toast, toastError } = useToast()

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
      .catch((err) => toastError(err instanceof Error ? err.message : '运维概览加载失败'))
      .finally(() => setLoading(false))
  }, [toastError])

  /** 下载数据库一致性快照（浏览器侧触发保存） */
  async function handleDownload() {
    setDownloading(true)
    try {
      await downloadMaintenanceBackup()
      toast('备份已开始下载')
    } catch (err) {
      toastError(err instanceof Error ? err.message : '备份下载失败')
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
      toastError(err instanceof Error ? err.message : '备份校验失败')
    } finally {
      setInspecting(false)
    }
  }

  /** 折扣分组重试率的列定义 */
  const retryRatioColumns: Column<MaintenanceRetryRatio>[] = [
    { title: '分组', render: (row) => <span className="font-medium text-ink">{row.group}</span> },
    { title: '倍率', align: 'right', render: (row) => <span className="tabular-nums text-ink-2">{row.ratio}%</span> },
    { title: '上游调用', align: 'right', render: (row) => <span className="tabular-nums text-ink-2">{formatNumber(row.upstream_calls)}</span> },
    { title: '计费请求', align: 'right', render: (row) => <span className="tabular-nums text-ink-2">{formatNumber(row.charged_requests)}</span> },
    {
      title: '重试率 r',
      align: 'right',
      render: (row) => (
        <span className={`tabular-nums font-medium ${row.over_break_even ? 'text-err' : 'text-ink-2'}`}>
          {row.retry_ratio.toFixed(3)}
        </span>
      ),
    },
    {
      title: '状态',
      align: 'center',
      render: (row) =>
        row.over_break_even ? <Badge tone="err">正在亏本</Badge> : <Badge tone="ok">正常</Badge>,
    },
  ]

  const usage24h = overview?.usage.last_24h
  const usage7d = overview?.usage.last_7d

  const tableColumns: Column<MaintenanceOverview['tables'][number]>[] = [
    { title: '表名', render: (row) => <span className="text-[13px] font-medium text-ink">{row.name}</span> },
    {
      title: '行数',
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatNumber(row.rows)}</span>,
    },
  ]

  const compareColumns: Column<MaintenanceCompareRow>[] = [
    { title: '表名', render: (row) => <span className="text-[13px] font-medium text-ink">{row.name}</span> },
    {
      title: '备份内',
      align: 'center',
      render: (row) => (row.in_backup ? <Badge tone="ok">存在</Badge> : <Badge tone="err">缺失</Badge>),
    },
    {
      title: '备份行数',
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{row.in_backup ? formatNumber(row.backup_rows) : '—'}</span>,
    },
    {
      title: '当前行数',
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatNumber(row.current_rows)}</span>,
    },
    {
      title: '差异',
      align: 'center',
      render: (row) =>
        row.in_backup && row.backup_rows !== row.current_rows ? (
          <Badge tone="warn">不一致</Badge>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
  ]

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">运维监控</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">运行健康度 · 数据库明细 · 备份管理</p>
      </div>

      {/* 顶部指标卡 */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="版本" value={overview ? overview.version : '—'} hint={overview?.build_time ? `构建 ${overview.build_time}` : undefined} />
        <StatCard
          label="启动时间"
          value={overview ? formatDateTime(overview.started_at) : '—'}
          hint={overview ? `已运行 ${formatUptime(overview.uptime_seconds)}` : undefined}
        />
        <StatCard
          label="Git 提交"
          value={overview && overview.git_commit ? overview.git_commit.slice(0, 8) : '—'}
          hint={overview?.git_commit || undefined}
        />
        <StatCard
          label="数据库体积"
          value={overview ? (overview.database.size_available ? formatMB(overview.database.size_bytes) : '—') : '—'}
          hint={overview ? `驱动 ${overview.database.driver}` : undefined}
        />
        <StatCard
          label="磁盘使用率"
          value={overview && overview.disk.available ? `${(overview.disk.used_ratio * 100).toFixed(1)}%` : '—'}
          hint={
            overview && overview.disk.available
              ? `可用 ${formatMB(overview.disk.free_bytes)} / 共 ${formatMB(overview.disk.total_bytes)}`
              : undefined
          }
        />
        <StatCard
          label="近 24h 请求"
          value={usage24h ? formatNumber(usage24h.requests) : '—'}
          hint={usage24h ? `失败 ${formatNumber(usage24h.failures)}（${(usage24h.failure_rate * 100).toFixed(1)}%）` : undefined}
        />
        <StatCard
          label="近 7d 请求"
          value={usage7d ? formatNumber(usage7d.requests) : '—'}
          hint={usage7d ? `失败 ${formatNumber(usage7d.failures)}（${(usage7d.failure_rate * 100).toFixed(1)}%）` : undefined}
        />
        <StatCard
          label="平均延迟"
          value={usage24h && usage24h.avg_latency_ms ? `${usage24h.avg_latency_ms} ms` : '—'}
          hint={usage7d && usage7d.avg_latency_ms ? `近 7d ${usage7d.avg_latency_ms} ms` : undefined}
        />
      </div>

      {/* 折扣分组重试率：r = 上游调用次数 / 计费请求次数。
          6 折档的净利本就薄，r 越过保本线（1.37）即正在亏本——必须让它可见。 */}
      {overview && overview.retry_ratios.length > 0 && (
        <Card padding="none">
          <div className="flex items-center justify-between border-b border-line px-4 py-3">
            <h2 className="text-sm font-semibold text-ink">折扣分组重试率</h2>
            <span className="text-xs text-ink-3">r 越过保本线即亏本；进程内累计，重启归零</span>
          </div>
          <DataTable
            columns={retryRatioColumns}
            rows={overview.retry_ratios}
            loading={false}
            rowKey={(row) => row.group}
            emptyTitle="暂无折扣分组调用"
          />
        </Card>
      )}

      {/* 数据库表行数 */}
      <Card padding="none">
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <h2 className="text-sm font-semibold text-ink">数据库表行数</h2>
        </div>
        <DataTable
          columns={tableColumns}
          rows={loading ? null : (overview?.tables ?? [])}
          loading={loading}
          rowKey={(row) => row.name}
          emptyTitle="未取到表信息"
        />
      </Card>

      {/* 备份管理 */}
      <Card padding="none">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3">
          <h2 className="text-sm font-semibold text-ink">备份管理</h2>
          <div className="flex items-center gap-2">
            <input ref={fileRef} type="file" accept=".db,.sqlite,.sqlite3,.bak" className="hidden" onChange={handleFile} />
            <Button variant="secondary" loading={inspecting} onClick={() => fileRef.current?.click()}>
              上传备份校验
            </Button>
            <Button variant="primary" loading={downloading} onClick={handleDownload}>
              下载备份
            </Button>
          </div>
        </div>

        <div className="p-4">
          {inspecting && !inspect ? (
            <SkeletonRows rows={2} />
          ) : inspect ? (
            <div className="space-y-4">
              <div className="flex flex-wrap items-center gap-3 text-[13px]">
                {inspect.valid ? <Badge tone="ok">校验通过</Badge> : <Badge tone="err">校验失败</Badge>}
                <span className="text-ink-2">
                  备份 schema v{inspect.schema_version} · 当前库 v{inspect.current_schema_version}
                </span>
                <span className="text-ink-3">
                  备份含迁移记录表：{inspect.schema_table_present ? '是' : '否'}
                </span>
              </div>
              {inspect.restore_steps.length > 0 && (
                <div className="rounded-md border border-line bg-surface p-3">
                  <div className="mb-1.5 text-xs font-medium text-ink-3">人工恢复步骤（后端不提供在线恢复）</div>
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
                emptyTitle="备份中无表数据"
              />
            </div>
          ) : (
            <div className="text-[13px] text-ink-3">
              选择「上传备份校验」检查一份备份文件的完整性与 schema 版本，确认后再人工按步骤恢复。
            </div>
          )}
        </div>
      </Card>

      {loading && !overview && <Skeleton className="h-40 w-full" />}
    </div>
  )
}