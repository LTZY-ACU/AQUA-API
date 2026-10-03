/** 数据表格：DataTable（列定义驱动）与 Pagination。
 *
 * 意图（Why）：
 *   后台几乎每页都是「筛选 + 表格 + 分页」范式；把表格收敛成列定义数组，
 *   页面只需声明列与渲染函数，保证列对齐（数字右对齐、文本左对齐）全站一致。
 */
'use client'

import type { ReactNode } from 'react'

import { AppIcon } from '@/components/AppIcon'
import { useI18n } from '@/i18n'
import { Button } from './Button'
import { EmptyState, SkeletonRows } from './Display'

export interface Column<T> {
  /** 列标题 */
  title: string
  /** 取值或自定义渲染 */
  render: (row: T) => ReactNode
  /** 对齐：默认左对齐；数字/状态建议传 right */
  align?: 'left' | 'right' | 'center'
  /** 列宽（tailwind 类，如 w-32）；默认等分 */
  width?: string
  /** 是否隐藏（响应式列） */
  className?: string
}

interface DataTableProps<T> {
  columns: Column<T>[]
  rows: T[] | null
  /** 行唯一键 */
  rowKey: (row: T) => string | number
  loading?: boolean
  emptyTitle?: string
  emptyDescription?: string
  onRowClick?: (row: T) => void
}

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  loading,
  emptyTitle,
  emptyDescription,
  onRowClick,
}: DataTableProps<T>) {
  const { t } = useI18n()
  const loadingState = loading || rows === null

  return (
    <div className="overflow-hidden rounded-lg border border-line bg-card">
      <div className="overflow-x-auto">
        <table className="w-full border-collapse text-sm">
          <thead>
            <tr className="border-b border-line bg-surface/70 text-[13px] text-ink-3">
              {columns.map((col, index) => (
                <th
                  key={index}
                  className={`px-4 py-2.5 font-medium ${
                    col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : 'text-left'
                  } ${col.width ?? ''}`}
                >
                  {col.title}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {loadingState ? (
              <tr>
                <td colSpan={columns.length} className="px-4 py-4">
                  <SkeletonRows rows={3} />
                </td>
              </tr>
            ) : rows && rows.length > 0 ? (
              rows.map((row) => (
                <tr
                  key={rowKey(row)}
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                  className={`border-b border-line/70 transition last:border-0 ${
                    onRowClick ? 'cursor-pointer hover:bg-surface/60' : 'hover:bg-surface/40'
                  }`}
                >
                  {columns.map((col, index) => (
                    <td
                      key={index}
                      className={`px-4 py-3 text-ink-2 ${
                        col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : 'text-left'
                      } ${col.className ?? ''}`}
                    >
                      {col.render(row)}
                    </td>
                  ))}
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={columns.length}>
                  <EmptyState
                    title={emptyTitle ?? t('components.dataState.empty')}
                    description={emptyDescription}
                  />
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

/* ── Pagination ─────────────────────────────────────────── */

interface PaginationProps {
  page: number
  pageSize: number
  total: number
  onChange: (page: number) => void
}

export function Pagination({ page, pageSize, total, onChange }: PaginationProps) {
  const { t } = useI18n()
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const pages: number[] = []
  const start = Math.max(1, page - 2)
  const end = Math.min(totalPages, page + 2)
  for (let i = start; i <= end; i++) pages.push(i)

  return (
    <div className="flex items-center justify-between gap-3 pt-3">
      {/* 刻意用词条里的 range + page 两句拼接，而不是把整句塞进一个键：
            后两种语言的语序（总数在前还是页码在前）不同，强行按中文语序拼会别扭。 */}
      <div className="text-xs text-ink-3">
        {t('components.pagination.range', {
          start: total === 0 ? 0 : (page - 1) * pageSize + 1,
          end: Math.min(page * pageSize, total),
          total,
        })}
        {' · '}
        {t('components.pagination.page', { page, pages: totalPages })}
      </div>
      <div className="flex items-center gap-1">
        <Button
          variant="ghost"
          size="sm"
          disabled={page <= 1}
          onClick={() => onChange(page - 1)}
          aria-label={t('components.pagination.prev')}
        >
          <AppIcon name="chevron-left" size={16} />
        </Button>
        {pages.map((p) => (
          <button
            key={p}
            type="button"
            onClick={() => onChange(p)}
            className={`min-w-7 rounded px-1.5 py-1 text-[13px] transition ${
              p === page ? 'bg-brand text-on-brand' : 'text-ink-3 hover:bg-ink/5 hover:text-ink'
            }`}
          >
            {p}
          </button>
        ))}
        <Button
          variant="ghost"
          size="sm"
          disabled={page >= totalPages}
          onClick={() => onChange(page + 1)}
          aria-label={t('components.pagination.next')}
        >
          <AppIcon name="chevron-right" size={16} />
        </Button>
      </div>
    </div>
  )
}