/** 管理后台：模型测速（/admin/speedtest）。
 *
 * 意图（Why）：
 *   渠道页的行内测速解决"测这一条渠道"；本页解决"挑渠道、挑模型、看排行"——
 *   站长横向对比时需要：选一个渠道 → 勾选要测的模型 → 一键测完看首字延迟排行。
 *   两者共用同一个运行器（useSpeedTestRunner）与后端接口，口径天然一致。
 *
 * 流转（Flow）：
 *   本页 → listChannels（渠道下拉）→ useSpeedTestRunner
 *       → speedTestChannel(id, [model])（逐模型请求，实时回报）
 *
 * 扩展（Extend）：
 *   需要跨渠道对比时，把"渠道下拉"换成多选并把 runner 按渠道循环即可；
 *   后端接口按渠道组织，不需要为跨渠道改协议。
 */
'use client'

import { useEffect, useMemo, useState } from 'react'

import { listChannels } from '@/api/admin'
import type { Channel } from '@/api/types'
import { Badge, Card, EmptyState } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { useSpeedTestRunner } from '@/components/admin/useSpeedTestRunner'
import { formatLatency } from '@/utils/format'

export default function AdminSpeedTestPage() {
  const [channels, setChannels] = useState<Channel[]>([])
  const [channelId, setChannelId] = useState<number | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())

  // 渠道清单：测速页只关心"有哪些渠道可选"，一页拉够（与渠道页一致的上限）。
  useEffect(() => {
    listChannels({ page: 1, size: 200 })
      .then((data) => {
        setChannels(data.items)
        if (data.items.length > 0) setChannelId((prev) => prev ?? data.items[0].id)
      })
      .catch(() => setChannels([]))
  }, [])

  const channel = useMemo(() => channels.find((c) => c.id === channelId) ?? null, [channels, channelId])
  const models = useMemo(() => channel?.models ?? [], [channel])

  // 切换渠道或模型清单变化时：默认全选（测速页的典型诉求就是"全测一遍看排行"）。
  useEffect(() => {
    setSelected(new Set(models))
  }, [models])

  const selectedModels = useMemo(() => models.filter((m) => selected.has(m)), [models, selected])

  const runner = useSpeedTestRunner(channelId, selectedModels)
  const doneEntries = runner.entries.filter((e) => e.status === 'done')
  const okEntries = doneEntries.filter((e) => e.result?.ok)
  const summary = useMemo(() => {
    if (okEntries.length === 0) return null
    const sorted = [...okEntries].sort((a, b) => (a.result?.ttfb_ms ?? 0) - (b.result?.ttfb_ms ?? 0))
    return {
      fastest: sorted[0],
      slowest: sorted[sorted.length - 1],
      avg: Math.round(okEntries.reduce((sum, e) => sum + (e.result?.ttfb_ms ?? 0), 0) / okEntries.length),
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runner.entries])

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">模型测速</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">
          逐模型测首字延迟（每次约消耗 2~3 token）；结果落库并展示在模型广场
        </p>
      </div>

      <Card padding="md">
        <div className="flex flex-wrap items-end gap-4">
          <label className="flex flex-col gap-1 text-[13px] text-ink-2">
            渠道
            <select
              value={channelId ?? ''}
              onChange={(e) => setChannelId(e.target.value ? Number(e.target.value) : null)}
              className="min-w-56 rounded-md border border-line bg-card px-3 py-1.5 text-[13px] text-ink"
            >
              {channels.length === 0 && <option value="">暂无渠道</option>}
              {channels.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}（{c.models.length} 个模型）
                </option>
              ))}
            </select>
          </label>

          <div className="flex items-center gap-2 text-[13px]">
            <Button variant="secondary" onClick={() => setSelected(new Set(models))} disabled={runner.running || models.length === 0}>
              全选
            </Button>
            <Button variant="secondary" onClick={() => setSelected(new Set())} disabled={runner.running}>
              清空
            </Button>
            <span className="text-ink-3">已选 {selectedModels.length}/{models.length}</span>
          </div>

          <div className="ml-auto flex items-center gap-2">
            {!runner.running ? (
              <Button variant="primary" onClick={runner.start} disabled={selectedModels.length === 0}>
                {runner.done === 0 ? '开始测速' : '继续测速'}
              </Button>
            ) : (
              <Button variant="secondary" onClick={runner.stop}>停止</Button>
            )}
            <Button variant="secondary" onClick={runner.reset} disabled={runner.running}>重置</Button>
          </div>
        </div>

        {runner.batchMessage && (
          <div className="mt-3 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-[13px] text-ink">
            {runner.batchMessage}
          </div>
        )}

        {models.length > 0 && (
          <div className="mt-3 max-h-40 overflow-y-auto rounded-md border border-line bg-surface p-3">
            <div className="flex flex-wrap gap-x-4 gap-y-1.5">
              {models.map((m) => (
                <label key={m} className="flex cursor-pointer items-center gap-1.5 text-[13px] text-ink-2">
                  <input
                    type="checkbox"
                    checked={selected.has(m)}
                    onChange={(e) => {
                      setSelected((prev) => {
                        const next = new Set(prev)
                        if (e.target.checked) next.add(m)
                        else next.delete(m)
                        return next
                      })
                    }}
                    className="accent-[var(--brand)]"
                  />
                  <span className="font-mono">{m}</span>
                </label>
              ))}
            </div>
          </div>
        )}
      </Card>

      {/* 结果区 */}
      <Card padding="none">
        {runner.entries.length === 0 ? (
          <div className="p-6">
            <EmptyState
              title={channel ? '选择模型后开始测速' : '先在上方选择渠道'}
              description="测速结果按首字延迟排序，成功在前"
            />
          </div>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-line px-4 py-3 text-[13px] text-ink-2">
              <span>
                进度 <span className="font-mono text-ink">{runner.done}</span>/{runner.entries.length}
              </span>
              <span>
                成功 <span className="font-mono text-ok">{runner.okCount}</span>
              </span>
              {summary && (
                <>
                  <span>
                    最快 <span className="font-mono text-ink">{summary.fastest.model}</span>
                    <span className="ml-1 font-mono text-ok">{formatLatency(summary.fastest.result?.ttfb_ms)}</span>
                  </span>
                  <span>
                    最慢 <span className="font-mono text-ink">{summary.slowest.model}</span>
                    <span className="ml-1 font-mono text-warn">{formatLatency(summary.slowest.result?.ttfb_ms)}</span>
                  </span>
                  <span>
                    平均 <span className="font-mono text-ink">{formatLatency(summary.avg)}</span>
                  </span>
                </>
              )}
              {runner.running && <span className="text-ink-3">测速中…</span>}
            </div>
            <SpeedResultTable entries={runner.entries} />
          </>
        )}
      </Card>
    </div>
  )
}

/** 结果表：完成的按 TTFB 升序在前（排行是本页的核心产出），未完成的按原顺序垫底 */
function SpeedResultTable({ entries }: { entries: ReturnType<typeof useSpeedTestRunner>['entries'] }) {
  const rows = useMemo(() => {
    const ranked = [...entries].sort((a, b) => rank(a) - rank(b))
    return ranked
  }, [entries])

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[13px]">
        <thead className="bg-surface text-ink-3">
          <tr>
            <th className="px-4 py-2 text-left font-medium">#</th>
            <th className="px-4 py-2 text-left font-medium">模型</th>
            <th className="px-4 py-2 text-left font-medium">状态</th>
            <th className="px-4 py-2 text-right font-medium">首字延迟</th>
            <th className="px-4 py-2 text-right font-medium">总耗时</th>
            <th className="px-4 py-2 text-right font-medium">状态码</th>
            <th className="px-4 py-2 text-left font-medium">说明</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((entry, index) => (
            <tr key={entry.model} className="border-t border-line">
              <td className="px-4 py-2 font-mono text-ink-3">{entry.status === 'done' && entry.result?.ok ? index + 1 : '—'}</td>
              <td className="px-4 py-2 font-mono text-ink" title={entry.result?.upstream_model}>{entry.model}</td>
              <td className="px-4 py-2">
                {entry.status === 'pending' && <Badge tone="off">待测</Badge>}
                {entry.status === 'running' && <Badge tone="warn">测速中</Badge>}
                {entry.status === 'done' && entry.result && (entry.result.ok ? <Badge tone="ok">成功</Badge> : entry.result.blocked ? <Badge tone="warn">已移除</Badge> : <Badge tone="err">失败</Badge>)}
              </td>
              <td className="px-4 py-2 text-right font-mono tabular-nums text-ink">
                {entry.result?.ok ? formatLatency(entry.result.ttfb_ms) : '—'}
              </td>
              <td className="px-4 py-2 text-right font-mono tabular-nums text-ink-3">
                {entry.result ? formatLatency(entry.result.total_ms) : '—'}
              </td>
              <td className="px-4 py-2 text-right font-mono tabular-nums text-ink-3">
                {entry.result?.status_code || '—'}
              </td>
              <td className="max-w-64 truncate px-4 py-2 text-ink-3" title={entry.result?.message}>
                {entry.result?.message || ''}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** 排序权重：成功的按 TTFB 升序，失败/未测垫底保持原顺序 */
function rank(entry: ReturnType<typeof useSpeedTestRunner>['entries'][number]): number {
  if (entry.status === 'done' && entry.result?.ok) return entry.result.ttfb_ms
  return Number.MAX_SAFE_INTEGER
}
