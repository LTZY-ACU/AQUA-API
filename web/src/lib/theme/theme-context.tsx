/** 主题 Provider：三套配色（浅色 / 深色 / 深蓝）+ 北京时间（UTC+8）自动切换。
 *
 * 意图（Why）：
 *   全站三套令牌（globals.css 的 @theme 浅色、html.dark 深色、
 *   html.dark[data-theme=navy] 深蓝）由本 Provider 驱动。
 *   默认 auto 模式跟随北京时间自动切换——白天浅色、夜晚深色；
 *   用户也可随时手动锁定任意一套（浅色 / 深色 / 深蓝）。
 *
 *   把「明暗语义」与「配色方案」拆开是这套设计的关键：
 *     scheme（light/dark）决定 dark: 工具类、color-scheme 与图表取色；
 *     palette（light/dark/navy）决定具体色值。
 *   深蓝复用 dark 语义（同时挂 .dark 与 data-theme=navy），因此全站既有的
 *   暗色适配一行都不用改——这是它能零成本落地的唯一原因。
 *
 * 流转（Flow）：
 *   root layout 内联脚本（首帧防闪烁：按 localStorage + 北京时间预设 class 与 data-theme）
 *   → ThemeProvider 挂载（读取真实状态、每分钟校正、跨昼夜分界自动换肤）
 *   → ThemeToggle（用户手动切 auto/light/dark/navy，写 localStorage）
 *
 * 扩展（Extend）：
 *   新增配色：ThemeMode/ResolvedTheme 加值 + globals.css 加令牌块 +
 *   applyTheme 里的 data-theme 映射 + ThemeToggle 的 OPTIONS 加一项，四处同步。
 */
'use client'

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'

export type ThemeMode = 'auto' | 'light' | 'dark' | 'navy'
export type ResolvedTheme = 'light' | 'dark' | 'navy'

export const THEME_STORAGE_KEY = 'aqua.theme'
/** 白天区间（北京时间小时）：[DAY_START, DAY_END) 视为日间，其余为夜间 */
export const DAY_START = 6
export const DAY_END = 18

/**
 * 该配色是否属于「暗色语义」。
 *
 * 图表（ECharts 走 canvas，不认 CSS 变量）必须靠它取色；
 * 页面里任何 resloved === 'dark' 的判断也都应改用它，
 * 否则新增配色时必然漏掉一处（深蓝下图表会退化成浅色配色，直接不可读）。
 */
export function isDarkScheme(theme: ResolvedTheme): boolean {
  return theme === 'dark' || theme === 'navy'
}

interface ThemeContextValue {
  /** 用户选择的模式 */
  mode: ThemeMode
  /** 实际生效的配色（auto 时由北京时间推导） */
  resolved: ResolvedTheme
  /** 当前北京时间（小时，含小数，供 UI 显示与说明「正在跟随」） */
  beijingHour: number
  setMode: (m: ThemeMode) => void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

/** 取北京时间的小时数（含小数）。与设备时区无关：按 UTC+8 计算。 */
export function beijingHourNow(d: Date = new Date()): number {
  const utcMs = d.getTime() + d.getTimezoneOffset() * 60_000
  const bj = new Date(utcMs + 8 * 3_600_000)
  return bj.getHours() + bj.getMinutes() / 60
}

/**
 * 由模式 + 北京时间推导实际配色。
 *
 * 自动模式只在「浅色 / 深色」之间选，不会切到深蓝：
 * 自动的价值在于可预测，若夜里有时深色有时深蓝，用户只会觉得主题在乱跳。
 */
export function resolveTheme(mode: ThemeMode, hour: number = beijingHourNow()): ResolvedTheme {
  if (mode === 'light' || mode === 'dark' || mode === 'navy') return mode
  return hour >= DAY_START && hour < DAY_END ? 'light' : 'dark'
}

function readStoredMode(): ThemeMode {
  if (typeof window === 'undefined') return 'auto'
  const v = window.localStorage.getItem(THEME_STORAGE_KEY)
  return v === 'light' || v === 'dark' || v === 'navy' || v === 'auto' ? v : 'auto'
}

/** 应用配色到 <html>；animate=true 时临时挂 .theme-anim 让颜色平滑过渡 */
function applyTheme(resolved: ResolvedTheme, animate: boolean) {
  const el = document.documentElement
  if (animate) {
    el.classList.add('theme-anim')
    window.setTimeout(() => el.classList.remove('theme-anim'), 700)
  }
  // 深蓝也挂 .dark（复用暗色语义），具体色值由 data-theme 区分
  el.classList.toggle('dark', isDarkScheme(resolved))
  el.dataset.theme = resolved
  el.style.colorScheme = isDarkScheme(resolved) ? 'dark' : 'light'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [mode, setModeState] = useState<ThemeMode>('auto')
  const [resolved, setResolved] = useState<ResolvedTheme>('light')
  const [beijingHour, setBeijingHour] = useState(12)
  const mounted = useRef(false)

  // 挂载后读取真实模式与时间（首帧样式由 root layout 内联脚本预设，避免闪烁）
  useEffect(() => {
    const m = readStoredMode()
    const h = beijingHourNow()
    const r = resolveTheme(m, h)
    setModeState(m)
    setBeijingHour(h)
    setResolved(r)
    applyTheme(r, false)
    mounted.current = true
  }, [])

  // auto 模式：每分钟校正一次，跨过昼夜分界时自动换肤（带过渡）
  useEffect(() => {
    if (mode !== 'auto') return
    const tick = () => {
      const h = beijingHourNow()
      setBeijingHour(h)
      const r = resolveTheme('auto', h)
      setResolved((prev) => {
        if (prev !== r && mounted.current) applyTheme(r, true)
        return r
      })
    }
    const id = window.setInterval(tick, 60_000)
    return () => window.clearInterval(id)
  }, [mode])

  const setMode = useCallback((m: ThemeMode) => {
    if (typeof window !== 'undefined') window.localStorage.setItem(THEME_STORAGE_KEY, m)
    const h = beijingHourNow()
    setModeState(m)
    setBeijingHour(h)
    setResolved(resolveTheme(m, h))
    applyTheme(resolveTheme(m, h), true)
  }, [])

  const value = useMemo(
    () => ({ mode, resolved, beijingHour, setMode }),
    [mode, resolved, beijingHour, setMode],
  )

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme 必须在 ThemeProvider 内使用')
  return ctx
}

/** 供 root layout 内联注入的首帧脚本：在 hydration 前按 localStorage + 北京时间
 *  预设 html.dark 与 data-theme，避免「先亮后暗」或「先深色后深蓝」的闪烁。
 *  与 ThemeProvider 的 resolveTheme/applyTheme 保持一致（改一处必须改另一处）。 */
export const THEME_INIT_SCRIPT = `(function(){try{
var m=localStorage.getItem('${THEME_STORAGE_KEY}')||'auto';
var h=new Date(Date.now()+8*3600000).getUTCHours();
var t=(m==='navy'||m==='dark'||m==='light')?m:((h<${DAY_START}||h>=${DAY_END})?'dark':'light');
var dark=(t==='dark'||t==='navy');
var e=document.documentElement;
if(dark)e.classList.add('dark');
e.dataset.theme=t;
e.style.colorScheme=dark?'dark':'light';
}catch(_){}})();`
