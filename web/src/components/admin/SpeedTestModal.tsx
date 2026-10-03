/** 模型测速弹层：对单个渠道的模型逐个测首字延迟（TTFB），实时亮结果。
 *
 * 意图（Why）：
 *   渠道测活只回答"通不通"（测第一个能用的模型），而站长排障与选路时
 *   更想知道"每个模型各有多快"。本弹层把逐模型测速做成可观察的过程：
 *   测完一个亮一个、随时可停——而不是按下一个按钮后盯着转圈等几分钟。
 *
 *   token 成本提示：每个模型一次真实调用，提示词 "ping" + max_tokens=1
 *   ≈ 消耗 2~3 token；拿到首字后连接立即断开，不再多耗。弹层固定展示
 *   这条说明，避免管理员误以为测速会产生大额上游费用。
 *
 * 流转（Flow）：
 *   channels/page.tsx → <SpeedTestModal/> → useSpeedTestRunner
 *     → speedTestChannel(id, [model])（每模型一个请求）
 *
 * 扩展（Extend）：
 *   独立测速页（/admin/speedtest）复用同一 hook；需要批量跨渠道测速时
 *   在页面层循环调用本弹层的能力即可，不必改后端。
 */
'use client'

import { useMemo, useState } from 'react'

import type { SpeedTestItem } from '@/api/types'
import { Badge } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Modal } from '@/components/ui/Modal'
import { useSpeedTestRunner, type SpeedTestEntry } from '@/components/admin/useSpeedTestRunner'
import { formatLatency } from '@/utils/format'

export function SpeedTestModal({
  open,
  channelId,
  channelName,
  models,
  onClose,
}: {
  open: boolean
  channelId: number | null
  channelName?: string
  models: string[]
  onClose: () => void
}) {
  const runner = useSpeedTestRunner(open ? channelId : null, open ? models : [])
  const [sorted, setSorted] = useState(false)

  // 展示清单：默认保持渠道声明顺序（与后台配置对照最直观）；
  // 勾选"按延迟排序"后成功的在前、按 TTFB 升序，失败垫底（失败没有 TTFB 可比）。
  const display = useMemo(() => {
    if (!sorted) return runner.entries
    return [...runner.entries].sort((a, b) => entryRank(a) - entryRank(b))
  }, [runner.entries, sorted])

  const { running } = runner
  const { done, okCount, total } = runnerProgress(runner.entries)

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`模型测速${channelName ? ` · ${channelName}` : ''}`}
      width={760}
    >
      <div className="space-y-4">
        <div className="rounded-md border border-line bg-surface p-3 text-xs text-ink-3">
          逐模型发送最小请求测<strong className="text-ink-2">首字延迟</strong>
          （提示词 ping + 输出截断 1 token，每个模型约消耗 2~3 个 token，
          测到首字即断开）。结果会保存并在模型广场展示；
          上游明确拒绝（403/404）的模型将自动从渠道移除。
        </div>

        {runner.batchMessage && (
          <div className="rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-[13px] text-ink">
            {runner.batchMessage}
          </div>
        )}

        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-3 text-[13px] text-ink-2">
            <span>
              进度 <span className="font-mono text-ink">{done}</span>/{total}
            </span>
            <span>
              成功 <span className="font-mono text-ok">{okCount}</span>
            </span>
            {running && <span className="text-ink-3">测速中…</span>}
          </div>
          <div className="flex items-center gap-2">
            <label className="flex cursor-pointer items-center gap-1.5 text-[13px] text-ink-3">
              <input
                type="checkbox"
                checked={sorted}
                onChange={(e) => setSorted(e.target.checked)}
                className="accent-[var(--brand)]"
              />
              按延迟排序
            </label>
            {!running ? (
              <Button variant="primary" onClick={runner.start} disabled={done === total}>
                {done === 0 ? '开始测速' : '继续测速'}
              </Button>
            ) : (
              <Button variant="secondary" onClick={runner.stop}>停止</Button>
            )}
            <Button variant="secondary" onClick={runner.reset} disabled={running}>重置</Button>
          </div>
        </div>

        <div className="max-h-80 overflow-y-auto rounded-md border border-line">
          <table className="w-full text-[13px]">
            <thead className="sticky top-0 bg-surface text-ink-3">
              <tr>
                <th className="px-3 py-2 text-left font-medium">模型</th>
                <th className="px-3 py-2 text-left font-medium">状态</th>
                <th className="px-3 py-2 text-right font-medium">首字延迟</th>
                <th className="px-3 py-2 text-right font-medium">总耗时</th>
                <th className="px-3 py-2 text-left font-medium">说明</th>
              </tr>
            </thead>
            <tbody>
              {display.map((entry) => (
                <SpeedTestRow key={entry.model} entry={entry} />
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </Modal>
  )
}

function SpeedTestRow({ entry }: { entry: SpeedTestEntry }) {
  const result = entry.result
  return (
    <tr className="border-t border-line">
      <td className="px-3 py-2 font-mono text-ink" title={result?.upstream_model}>
        {entry.model}
      </td>
      <td className="px-3 py-2">
        {entry.status === 'pending' && <Badge tone="off">待测</Badge>}
        {entry.status === 'running' && <Badge tone="warn">测速中</Badge>}
        {entry.status === 'done' && result && (result.ok ? <Badge tone="ok">成功</Badge> : result.blocked ? <Badge tone="warn">已移除</Badge> : <Badge tone="err">失败</Badge>)}
      </td>
      <td className="px-3 py-2 text-right font-mono tabular-nums text-ink">
        {result?.ok ? latencyText(result.ttfb_ms) : '—'}
      </td>
      <td className="px-3 py-2 text-right font-mono tabular-nums text-ink-3">
        {result ? latencyText(result.total_ms) : '—'}
      </td>
      <td className="max-w-56 truncate px-3 py-2 text-ink-3" title={result?.message}>
        {result?.message || ''}
      </td>
    </tr>
  )
}

/** 排序权重：成功的按 TTFB 升序在前，其余按原顺序垫底 */
function entryRank(entry: SpeedTestEntry): number {
  if (entry.status === 'done' && entry.result?.ok) return entry.result.ttfb_ms
  return Number.MAX_SAFE_INTEGER
}

/** 统计进度与成功数 */
function runnerProgress(entries: SpeedTestEntry[]) {
  const done = entries.filter((e) => e.status === 'done').length
  const okCount = entries.filter((e) => e.status === 'done' && e.result?.ok).length
  return { done, okCount, total: entries.length }
}

/** 延迟展示：0 显示为 "—"（0 在本场景意味着没有拿到数据） */
function latencyText(ms: number): string {
  if (!ms || ms <= 0) return '—'
  return formatLatency(ms)
}
