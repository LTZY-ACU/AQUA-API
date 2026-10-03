/** 模型广场（公开 /models）：分组筛选 + 表格化模型清单 + 详情弹层。
 *
 * 意图（Why）：
 *   用户要「技术站」，因此把原来的卡片网格改成**表格**——一行一个模型，
 *   列固定为「模型 / 分组 / 价格 / 渠道」，扫视与比价都更快，也更像工程清单。
 *
 *   代理视图：被指派了代理分组的账号登录后，看到的是【自己那一档】的模型与价格，
 *   并额外给出「原价划线 + 橙色折扣块」的对照——原价是他对外报价的锚，
 *   折后价是他自己的拿货成本，两者并排才能一眼算出毛利。
 *   普通用户/匿名访客看到的仍是公开模型与公开价，完全看不出批发档的存在。
 *
 * 流转（Flow）：
 *   SiteHeader → 分组筛选(Tabs) + 搜索 → 表格（按厂商分区）→ 行点击 → 详情弹层 → SiteFooter
 */
'use client'

import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'

import { fetchModelPlaza } from '@/api/site'
import type { ModelPlaza, PlazaModel, PlazaPrice, PlazaViewer } from '@/api/types'
import { AppIcon } from '@/components/AppIcon'
import { ModelDetailModal } from '@/components/plaza/ModelDetailModal'
import { priceSummaryLabel } from '@/components/plaza/pricing'
import { SiteFooter } from '@/components/site/SiteFooter'
import { SiteHeader } from '@/components/site/SiteHeader'
import { Badge, EmptyState, Skeleton, Tabs } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { useSite } from '@/lib/site/site-context'
import { formatLatency } from '@/utils/format'
import { formatDiscountLabel } from '@/utils/money'
import { vendorLabel, vendorOf, vendorTone } from '@/utils/vendor'

export default function ModelPlazaPage() {
  const [data, setData] = useState<ModelPlaza | null>(null)
  const [error, setError] = useState(false)
  const [group, setGroup] = useState('all')
  const [keyword, setKeyword] = useState('')
  const [selected, setSelected] = useState<PlazaModel | null>(null)

  const load = useCallback(async () => {
    try {
      const result = await fetchModelPlaza({ group: group === 'all' ? undefined : group, keyword: keyword || undefined })
      setData(result)
      setError(false)
    } catch {
      // 失败必须让用户看得见并能重试，而不是一直停在加载骨架（会让人以为页面卡死）
      setData(null)
      setError(true)
    }
  }, [group, keyword])

  useEffect(() => {
    void load()
  }, [load])

  const viewer = data?.viewer

  /** 按厂商分区，便于在长表里按相似性定位 */
  const grouped = useMemo(() => {
    if (!data) return []
    const map = new Map<string, PlazaModel[]>()
    for (const item of data.items) {
      const vendor = vendorOf(item.model)
      const list = map.get(vendor) ?? []
      list.push(item)
      map.set(vendor, list)
    }
    return [...map.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  }, [data])

  const tabs = useMemo(() => {
    const items: { value: string; label: string; count?: number }[] = [{ value: 'all', label: '全部' }]
    for (const g of data?.groups ?? []) {
      items.push({ value: g.name, label: g.label, count: g.model_count })
    }
    return items
  }, [data])

  return (
    <>
      <SiteHeader />
      <main className="mx-auto max-w-6xl px-4 py-10 sm:px-6">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <div className="font-mono text-[12px] text-ink-3">
              <span className="text-brand">/</span> models
            </div>
            <h1 className="mt-1.5 text-2xl font-bold tracking-tight text-ink">模型与价格</h1>
            <p className="mt-1 text-[13px] text-ink-3">
              {viewer ? '你看到的为代理拿货档的模型与折扣价。' : '当前可用模型与分组价格，实时来自站点信息。'}
            </p>
          </div>
          <div className="flex max-w-xs flex-1 items-center gap-2 rounded-md border border-line-2 bg-card px-3 focus-within:border-brand">
            <AppIcon name="search" size={15} className="text-ink-3" />
            <input
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              placeholder="搜索模型…"
              className="h-9 flex-1 bg-transparent text-sm outline-none placeholder:text-ink-3"
            />
          </div>
        </div>

        {viewer && <AgentBanner viewer={viewer} />}

        {/* 代理视图只涉及单一档位，分组筛选没有意义，直接隐藏避免误导 */}
        {!viewer && (
          <div className="mt-6">
            <Tabs items={tabs as { value: string; label: string; count?: number }[]} value={group} onChange={(v) => setGroup(v)} />
          </div>
        )}

        {error ? (
          <div className="mt-6 rounded-lg border border-err/30 bg-err/5">
            <EmptyState
              title="模型广场加载失败"
              description="网络或服务暂时不可用，请稍后重试。"
              action={
                <Button variant="secondary" size="sm" onClick={() => void load()}>
                  重试
                </Button>
              }
            />
          </div>
        ) : !data ? (
          <div className="mt-6 overflow-hidden rounded-lg border border-line">
            {Array.from({ length: 8 }).map((_, i) => (
              <Skeleton key={i} className="h-11 w-full rounded-none border-b border-line" />
            ))}
          </div>
        ) : data.items.length === 0 ? (
          <div className="mt-6 rounded-lg border border-line bg-card">
            <EmptyState
              title="没有匹配的模型"
              description={viewer ? '当前代理档尚未配置模型价格，请联系管理员。' : '换一个关键词或分组试试'}
            />
          </div>
        ) : (
          <div className="mt-6 overflow-hidden rounded-lg border border-line">
            <table className="w-full text-left text-[13px]">
              <thead className="bg-surface">
                <tr className="font-mono text-[11px] uppercase tracking-wider text-ink-3">
                  <th className="px-4 py-2.5 font-normal">模型</th>
                  <th className="hidden px-4 py-2.5 font-normal sm:table-cell">分组</th>
                  <th className="px-4 py-2.5 text-right font-normal">价格</th>
                  <th className="hidden px-4 py-2.5 text-right font-normal md:table-cell">渠道</th>
                  <th className="hidden px-4 py-2.5 text-right font-normal md:table-cell">首字延迟</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {grouped.map(([vendor, models]) => (
                  <Fragment key={vendor}>
                    {/* 厂商分区行：跨越整表，给长表一个视觉锚点 */}
                    <tr className="bg-surface/60">
                      <td colSpan={5} className="px-4 py-1.5">
                        <span className="flex items-center gap-2">
                          <span className={`flex h-5 w-5 items-center justify-center rounded-full ring-1 text-[10px] font-semibold ${vendorTone(vendor)}`}>
                            {vendorLabel(vendor).slice(0, 1).toUpperCase()}
                          </span>
                          <span className="font-mono text-[11px] uppercase tracking-wider text-ink-3">{vendorLabel(vendor)}</span>
                          <span className="text-[11px] text-ink-3">· {models.length}</span>
                        </span>
                      </td>
                    </tr>
                    {models.map((model) => (
                      <ModelRow key={model.model} model={model} viewer={viewer} onOpen={() => setSelected(model)} />
                    ))}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <div className="mt-4 font-mono text-[12px] text-ink-3">共 {data?.total ?? 0} 个模型</div>
      </main>

      <SiteFooter />

      <ModelDetailModal model={selected} viewer={viewer} onClose={() => setSelected(null)} />
    </>
  )
}

/* ── 代理身份横幅：说明"你正在以什么身份看这份清单" ───────── */

function AgentBanner({ viewer }: { viewer: PlazaViewer }) {
  return (
    <div className="mt-6 flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-lg border border-warn/30 bg-warn/8 px-4 py-3">
      <Badge tone="warn">{viewer.label}</Badge>
      <span className="text-[13px] font-medium text-ink">代理拿货价</span>
      <span className="font-mono text-[12px] text-ink-2">
        基础价 × {viewer.ratio}% = {discountLabel(viewer.ratio)}
      </span>
      <span className="text-[12px] text-ink-3">下方价格已按你的拿货折扣结算，与原价并排展示。</span>
      {/* 分组 RPM 提示：仅当后端下发且 >0 时显示（0/缺失 = 不限速，不显示） */}
      {viewer.rpm_limit && viewer.rpm_limit > 0 ? (
        <span className="w-full text-[12px] text-ink-3">
          该分组每分钟请求上限：{viewer.rpm_limit} 次/分钟
        </span>
      ) : null}
    </div>
  )
}

/* ── 表格行 ─────────────────────────────────────────────── */

function ModelRow({ model, viewer, onOpen }: { model: PlazaModel; viewer?: PlazaViewer; onOpen: () => void }) {
  const { quotaPerYuan } = useSite()
  const price = model.prices?.[0]
  return (
    <tr className="cursor-pointer bg-card transition hover:bg-surface/60" onClick={onOpen}>
      <td className="px-4 py-2.5">
        <div className="flex items-center gap-2">
          <span className="font-mono text-ink">{model.model}</span>
          {model.available ? <Badge tone="ok">可用</Badge> : <Badge tone="err">不可用</Badge>}
        </div>
      </td>
      <td className="hidden px-4 py-2.5 font-mono text-ink-3 sm:table-cell">{model.groups?.join(' / ') || '—'}</td>
      <td className="px-4 py-2.5 text-right">
        {viewer ? (
          <AgentPriceCell price={price} listPrice={model.list_price} ratio={viewer.ratio} quotaPerYuan={quotaPerYuan} />
        ) : (
          <span className="text-ink-2">{priceSummaryLabel(price, quotaPerYuan)}</span>
        )}
      </td>
      <td className="hidden px-4 py-2.5 text-right font-mono text-ink-3 md:table-cell">{model.channel_count}</td>
      <td className="hidden px-4 py-2.5 text-right md:table-cell">
        <SpeedCell ms={model.speed_ttfb_ms} />
      </td>
    </tr>
  )
}

/**
 * 首字延迟单元格：后台「模型测速」的快照数据（非实时）。
 *
 * 未下发（未测过 / 站长关闭公示）时显示 "—"，与"渠道"列的空态保持一致；
 * 颜色分档：<1s 快（绿）、1~3s 正常（默认）、>3s 慢（橙），扫视即可分拣。
 */
function SpeedCell({ ms }: { ms?: number }) {
  if (!ms || ms <= 0) return <span className="font-mono text-ink-3">—</span>
  const tone = ms < 1000 ? 'text-ok' : ms < 3000 ? 'text-ink-2' : 'text-warn'
  return (
    <span className={`font-mono tabular-nums ${tone}`} title="后台测速快照（首字延迟，非实时）">
      {formatLatency(ms)}
    </span>
  )
}

/** 代理价单元格：原价划线 + 橙色折扣块（价格与折扣同框，一眼看清拿货成本） */
function AgentPriceCell({
  price,
  listPrice,
  ratio,
  quotaPerYuan,
}: {
  price: PlazaPrice | undefined
  listPrice: PlazaPrice | undefined
  ratio: number
  quotaPerYuan: number
}) {
  if (!price) return <span className="text-ink-3">待定价</span>
  return (
    <span className="inline-flex items-center justify-end gap-2 align-middle">
      {listPrice && (
        <span className="text-[12px] text-ink-3 line-through decoration-ink-3/70">
          {priceSummaryLabel(listPrice, quotaPerYuan)}
        </span>
      )}
      <span className="inline-flex items-center gap-1.5 rounded border border-warn/40 bg-warn/15 px-2 py-0.5 font-mono text-[12px] font-medium text-warn">
        {priceSummaryLabel(price, quotaPerYuan)}
        <span className="opacity-70">· {discountLabel(ratio)}</span>
      </span>
    </span>
  )
}

/** 倍率（百分比）→ 折扣文案：60 → 6折、95 → 9.5折、100 → 原价 */
const discountLabel = formatDiscountLabel
