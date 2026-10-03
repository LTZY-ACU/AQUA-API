/** EChart：echarts 的 React 容器（等价旧 EChart.vue）。
 *
 * 意图（Why）：
 *   统一图表初始化/销毁/resize 生命周期，页面只提供 option。
 *   取色来自 utils/chart.ts 的 chartStyles(isDark)，随昼夜主题切换；
 *   页面在 option 变化时通过下方第二个 effect 以 notMerge 重绘，实现换肤。
 */
'use client'

import { useEffect, useRef } from 'react'
import * as echarts from 'echarts/core'
import { LineChart, BarChart, PieChart } from 'echarts/charts'
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DatasetComponent,
  TitleComponent,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
// LegacyGridContainLabel：echarts v6 起 `grid.containLabel` 变为「需显式注册」的兼容特性，
// 不注册则该选项静默失效（Y 轴大数字会被裁切），并打印 "use LegacyGridContainLabel" 提示。
// 门户与后台趋势图都依赖 containLabel 自适应轴标签宽度，故在此统一注册，保持 v5 行为不变。
import { LegacyGridContainLabel } from 'echarts/features'

echarts.use([LineChart, BarChart, PieChart, GridComponent, TooltipComponent, LegendComponent, DatasetComponent, TitleComponent, CanvasRenderer, LegacyGridContainLabel])

interface EChartProps {
  option: echarts.EChartsCoreOption
  height?: number
  className?: string
}

export function EChart({ option, height = 280, className }: EChartProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<echarts.ECharts | null>(null)

  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const chart = echarts.init(el)
    chartRef.current = chart
    const observer = new ResizeObserver(() => chart.resize())
    observer.observe(el)
    return () => {
      observer.disconnect()
      chart.dispose()
      chartRef.current = null
    }
  }, [])

  useEffect(() => {
    chartRef.current?.setOption(option, { notMerge: true })
  }, [option])

  return <div ref={containerRef} className={className} style={{ height }} />
}