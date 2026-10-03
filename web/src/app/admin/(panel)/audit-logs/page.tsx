/** 管理后台：操作审计（/admin/audit-logs）。
 *
 * 意图（Why）：
 *   后台所有写操作（建渠道、改设置、删用户、退款……）都由后端审计中间件
 *   自动落库。本页把这些记录按条件查出来，回答「谁在什么时间对哪里做了什么、
 *   结果如何」，是安全追溯与误操作排查的依据。
 *
 * 流转（Flow）：
 *   本页 → listAuditLogs({page,page_size,admin_id,method,path,status_code})
 *        → GET /api/admin/audit-logs
 *
 * 扩展（Extend）：
 *   新增筛选条件：在 AuditLogQuery 加字段（后端 handler_audit.go 同步解析），
 *   再补一个输入框即可；分页参数是 page_size（接口主参数名）。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { listAuditLogs, type AuditLog } from '@/api/audit'
import { Badge, Card } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Form'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime, formatLatency } from '@/utils/format'

const PAGE_SIZE = 20

/** HTTP 状态码 → Badge 配色（与 display.ts 的 httpStatusBadgeClass 同口径，适配 React 版 Badge） */
function statusTone(code: number | undefined): 'ok' | 'warn' | 'err' | 'off' {
  if (!code) return 'off'
  if (code >= 200 && code < 300) return 'ok'
  if (code >= 400 && code < 500) return 'warn'
  if (code >= 500) return 'err'
  return 'off'
}

export default function AdminAuditLogsPage() {
  const [items, setItems] = useState<AuditLog[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)

  // 筛选条件（草稿态：点「查询」才并入请求）
  const [adminId, setAdminId] = useState('')
  const [method, setMethod] = useState('')
  const [path, setPath] = useState('')
  const [statusCode, setStatusCode] = useState('')

  const { toastError } = useToast()
  const { t } = useI18n()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listAuditLogs({
        page,
        page_size: PAGE_SIZE,
        admin_id: adminId.trim() ? Number(adminId.trim()) : undefined,
        method: method.trim() ? method.trim().toUpperCase() : undefined,
        path: path.trim() || undefined,
        status_code: statusCode.trim() ? Number(statusCode.trim()) : undefined,
      })
      setItems(data.items)
      setTotal(data.total)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '审计日志加载失败')
    } finally {
      setLoading(false)
    }
  }, [page, adminId, method, path, statusCode, toastError])

  useEffect(() => {
    void load()
  }, [load])

  /** 查询：页码归 1；若已在第 1 页则直接刷新 */
  function handleSearch() {
    setPage(1)
    if (page === 1) void load()
  }

  /** 重置：清空筛选并回到第 1 页 */
  function handleReset() {
    setAdminId('')
    setMethod('')
    setPath('')
    setStatusCode('')
    setPage(1)
    if (page === 1) void load()
  }

  const columns: Column<AuditLog>[] = [
    {
      title: '时间',
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
    },
    {
      title: '管理员',
      render: (row) => (
        <span className="text-[13px] text-ink-2">
          {row.admin_username || (row.admin_id > 0 ? `#${row.admin_id}` : '系统')}
        </span>
      ),
    },
    {
      title: '动作',
      render: (row) => <span className="text-[13px] font-medium text-ink">{row.action || row.method}</span>,
    },
    {
      title: '路径',
      render: (row) => (
        <span className="block max-w-64 truncate text-[13px]" title={row.path}>{row.path}</span>
      ),
    },
    {
      title: '目标',
      render: (row) => (
        <span className="block max-w-32 truncate text-[13px] text-ink-3" title={row.target}>{row.target || '—'}</span>
      ),
    },
    {
      title: '状态码',
      align: 'center',
      render: (row) => <Badge tone={statusTone(row.status_code)}>{row.status_code || '—'}</Badge>,
    },
    {
      title: '耗时',
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatLatency(row.latency_ms)}</span>,
    },
    {
      title: '来源 IP',
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-2">{row.client_ip || '—'}</span>,
    },
  ]

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">操作审计</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">后台写操作留痕（共 {total} 条）</p>
      </div>

      {/* 筛选条 */}
      <Card>
        <div className="flex flex-wrap items-end gap-3">
          <Field label="管理员 ID">
            <Input value={adminId} onChange={(e) => setAdminId(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && handleSearch()} placeholder="数字 ID" type="number" className="w-32" />
          </Field>
          <Field label="方法">
            <Input value={method} onChange={(e) => setMethod(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && handleSearch()} placeholder="POST / PUT / DELETE" className="w-36" />
          </Field>
          <Field label="路径前缀">
            <Input value={path} onChange={(e) => setPath(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && handleSearch()} placeholder="如 /admin/channels" className="w-56" />
          </Field>
          <Field label="状态码">
            <Input value={statusCode} onChange={(e) => setStatusCode(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && handleSearch()} placeholder="如 200" type="number" className="w-32" />
          </Field>
          <div className="flex items-center gap-2">
            <Button variant="primary" onClick={handleSearch}>查询</Button>
            <Button variant="secondary" onClick={handleReset}>重置</Button>
          </div>
        </div>
      </Card>

      {/* 列表 + 分页 */}
      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle="暂无审计记录"
          emptyDescription="后台写操作较少，或在筛选范围内没有记录。"
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>
    </div>
  )
}