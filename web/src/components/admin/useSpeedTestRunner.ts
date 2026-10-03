/**
 * 模型测速运行器：对指定渠道的模型逐个测首字延迟（TTFB），实时回报进度。
 *
 * 意图（Why）：
 *   测速要对上游产生真实（虽然极小）的请求，一次全量测完几百个模型会把
 *   HTTP 请求挂住几十分钟且看不到任何进度。因此这里在前端【逐模型循环调用】
 *   后端接口：每个模型一个独立请求，测完一个亮一个，随时可以停。
 *
 *   后端刻意串行探测（并发会让延迟互相污染，数字失去对比意义），
 *   前端这里的循环天然与之匹配——同一时刻上游只承受一个测速请求。
 *
 * 流转（Flow）：
 *   channels/page.tsx（行内测速弹层）、admin/speedtest/page.tsx（独立测速页）
 *     → useSpeedTestRunner(channelId, models)
 *         → speedTestChannel(id, [model]) 逐个调用
 *
 * 扩展（Extend）：
 *   需要并发（如按渠道分片并行）时改这里的循环即可，后端接口无需变动。
 */
'use client'

import { useCallback, useEffect, useRef, useState } from 'react'

import { speedTestChannel } from '@/api/admin'
import type { SpeedTestItem, SpeedTestRunResult } from '@/api/types'

/** 单个模型的运行状态：待测 → 测速中 → 已完成（成功/失败） */
export interface SpeedTestEntry {
  model: string
  status: 'pending' | 'running' | 'done'
  result?: SpeedTestItem
}

/** useSpeedTestRunner 返回的控制器 */
export interface SpeedTestRunner {
  /** 全部条目（保持模型清单顺序），组件据此渲染表格 */
  entries: SpeedTestEntry[]
  /** 是否正在运行 */
  running: boolean
  /** 已完成的条数（含失败） */
  done: number
  /** 成功条数 */
  okCount: number
  /** 整批级失败说明（凭据不可用、功能被关闭等）；非空时应停止循环并展示 */
  batchMessage: string | null
  /** 开始（从第一个 pending 的模型继续） */
  start: () => void
  /** 停止当前循环（正在测的那一个完成后即停） */
  stop: () => void
  /** 重置全部条目为待测 */
  reset: () => void
}

/**
 * 逐模型测速的循环控制器。
 *
 * 状态管理说明：entries 同时维护 state（供渲染）与 ref 镜像（供循环读
 * "下一个待测模型"）。不用 setState 的函数式更新来找目标——它的 updater
 * 在 React 18 中异步执行，循环里同步读不到，会误判"没有待测模型"。
 */
export function useSpeedTestRunner(channelId: number | null, models: string[]): SpeedTestRunner {
  const [entries, setEntries] = useState<SpeedTestEntry[]>([])
  const [running, setRunning] = useState(false)
  const [batchMessage, setBatchMessage] = useState<string | null>(null)
  const entriesRef = useRef<SpeedTestEntry[]>([])
  const stopRef = useRef(false)
  const loopRef = useRef(false)

  // 模型清单以「内容键」参与依赖，而不是数组身份。
  //
  // 为什么：调用方（如关闭状态的弹层）可能每次渲染都传入新的空数组/新引用；
  // 若直接以数组身份做 effect 依赖，会形成「渲染 → effect → setEntries(新数组)
  // → 再渲染」的死循环，把整个页面的 JS 线程冻住（表现为页面卡死、点击无响应）。
  // 用内容比较后，清单不变就不重建，身份抖动被完全吸收。
  const modelsKey = models.join('\u0000')

  // 模型清单变化（打开弹层 / 切渠道）时重建条目并复位状态。
  useEffect(() => {
    const fresh: SpeedTestEntry[] = models.map((model) => ({ model, status: 'pending' }))
    entriesRef.current = fresh
    setEntries(fresh)
    setRunning(false)
    setBatchMessage(null)
    stopRef.current = false
    loopRef.current = false
    // 依赖只认 channelId 与内容键；models 取当前渲染闭包中的最新值。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channelId, modelsKey])

  /** 唯一的条目更新入口：先写 ref 再写 state，两者永不错位。 */
  const applyEntries = useCallback((next: SpeedTestEntry[]) => {
    entriesRef.current = next
    setEntries(next)
  }, [])

  const stop = useCallback(() => {
    stopRef.current = true
  }, [])

  const reset = useCallback(() => {
    stopRef.current = true
    setRunning(false)
    setBatchMessage(null)
    const fresh: SpeedTestEntry[] = entriesRef.current.map((e) => ({ model: e.model, status: 'pending' }))
    entriesRef.current = fresh
    setEntries(fresh)
  }, [])

  const start = useCallback(() => {
    if (channelId == null || channelId <= 0) return
    if (loopRef.current) return // 已在循环中，避免重复启动
    loopRef.current = true
    stopRef.current = false
    setRunning(true)
    setBatchMessage(null)

    void (async () => {
      try {
        while (!stopRef.current) {
          const target = entriesRef.current.find((e) => e.status === 'pending')?.model
          if (!target) break

          applyEntries(
            entriesRef.current.map((e) =>
              e.model === target ? { ...e, status: 'running' as const } : e,
            ),
          )

          try {
            const result: SpeedTestRunResult = await speedTestChannel(channelId, [target])
            if (result.message && result.items.length === 0) {
              // 整批失败（凭据不可用 / 功能关闭）：停止循环并展示原因。
              setBatchMessage(result.message)
              applyEntries(
                entriesRef.current.map((e) =>
                  e.status === 'running' ? { ...e, status: 'pending' as const } : e,
                ),
              )
              break
            }
            const item = result.items[0]
            if (item) {
              applyEntries(
                entriesRef.current.map((e) =>
                  e.model === target ? { ...e, status: 'done', result: item } : e,
                ),
              )
            } else {
              // 后端没回条目（理论上不该发生）：按失败回退到 pending，可重试。
              applyEntries(
                entriesRef.current.map((e) =>
                  e.model === target ? { ...e, status: 'pending' as const } : e,
                ),
              )
            }
          } catch {
            // 网络层失败：把当前条目标记回 pending（可重试），停止循环。
            setBatchMessage('请求失败（网络错误或登录已过期），测速已停止')
            applyEntries(
              entriesRef.current.map((e) =>
                e.status === 'running' ? { ...e, status: 'pending' as const } : e,
              ),
            )
            break
          }
        }
      } finally {
        loopRef.current = false
        setRunning(false)
      }
    })()
  }, [channelId, applyEntries])

  const done = entries.filter((e) => e.status === 'done').length
  const okCount = entries.filter((e) => e.status === 'done' && e.result?.ok).length

  return { entries, running, done, okCount, batchMessage, start, stop, reset }
}
