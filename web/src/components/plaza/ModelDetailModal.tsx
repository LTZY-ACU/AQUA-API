/** 模型详情弹层：逐分组价格 + 计费方式 + 缓存价 + 倍率说明 + 定价试算。
 *
 * 意图（Why）：
 *   模型广场的列表只能给一行摘要，用户点进来最想弄清三件事：
 *     1) 这个模型到底怎么计费（免费 / 按次 / 按量）；
 *     2) 同一模型为什么在不同分组价格不同（分组倍率）；
 *     3) 我这个用量大概花多少钱（试算器）。
 *   本弹层把这三件事一次讲清，并逐分组列出价格（PlazaModel.prices 是数组）。
 *
 *   代理视图：代理登录后后端只下发他那一档的代理价 + 划线原价，
 *   这里保持「原价划线 + 橙色折扣块」的既有语言，并把"你当前是 XX 代理档（X 折）"写明。
 *
 * 流转（Flow）：
 *   models/page.tsx 选中模型 → <ModelDetailModal> → 价格表 + <ModelPriceCalculator>
 *
 * 扩展（Extend）：
 *   新增价格维度（如缓存写入价）时，在价格表的「价格」列补一行，
 *   并同步 pricing.ts 的换算函数，保持展示与试算同口径。
 */
'use client'

import type { PlazaModel, PlazaPrice, PlazaViewer } from '@/api/types'
import { AppIcon } from '@/components/AppIcon'
import { Badge } from '@/components/ui/Display'
import { Modal } from '@/components/ui/Modal'
import { formatDiscountLabel, formatYuanPerCall, formatYuanPerMillion } from '@/utils/money'
import { formatDateTime } from '@/utils/format'
import { useSite } from '@/lib/site/site-context'

import { ModelPriceCalculator } from './ModelPriceCalculator'
import { billingKindLabel, billingKindOf, cachePriceLabel, ratioLabel } from './pricing'

interface Props {
  model: PlazaModel | null
  viewer?: PlazaViewer
  onClose: () => void
}

export function ModelDetailModal({ model, viewer, onClose }: Props) {
  const { quotaPerYuan } = useSite()
  if (!model) return null

  return (
    <Modal open onClose={onClose} title={model.model} width={640}>
      <div className="flex flex-wrap items-center gap-2">
        {viewer && <Badge tone="warn">{viewer.label}</Badge>}
        {model.available ? <Badge tone="ok">可用</Badge> : <Badge tone="err">不可用</Badge>}
        <Badge tone="off">{model.channel_count} 个启用渠道</Badge>
        <Badge tone="info">{model.groups.length} 个分组</Badge>
      </div>

      {viewer && <AgentNote viewer={viewer} />}

      {model.prices.length > 0 ? (
        <>
          <div className="mt-4 overflow-hidden rounded-md border border-line">
            <table className="w-full text-left text-[13px]">
              <thead className="bg-surface">
                <tr className="font-mono text-[11px] uppercase tracking-wider text-ink-3">
                  <th className="px-3 py-2 font-normal">分组</th>
                  <th className="px-3 py-2 font-normal">计费方式</th>
                  <th className="px-3 py-2 text-right font-normal">{viewer ? '原价 / 代理拿货价' : '价格'}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {model.prices.map((price) => (
                  <PriceRow
                    key={price.group}
                    price={price}
                    listPrice={viewer ? model.list_price : undefined}
                    viewer={viewer}
                    quotaPerYuan={quotaPerYuan}
                  />
                ))}
              </tbody>
            </table>
          </div>
          {model.prices.length > 1 && !viewer && (
            <p className="mt-2 text-[11px] text-ink-3">
              同一模型在不同分组价格不同，差异来自各分组的计费倍率（倍率越低越便宜）。
            </p>
          )}
        </>
      ) : (
        <p className="mt-4 text-sm text-ink-3">该模型暂未配置价格规则。</p>
      )}

      <div className="mt-4">
        {/* key=模型名：切换模型时重置试算器的分组与用量输入 */}
        <ModelPriceCalculator
          key={model.model}
          prices={model.prices}
          viewer={viewer}
          quotaPerYuan={quotaPerYuan}
          modelName={model.model}
        />
      </div>
    </Modal>
  )
}

/* ── 代理身份说明：讲清"你当前按哪一档拿货" ──────────────── */

function AgentNote({ viewer }: { viewer: PlazaViewer }) {
  return (
    <div className="mt-3 rounded-md border border-warn/30 bg-warn/8 px-3.5 py-2.5 text-[12px] text-ink-2">
      你当前是「<span className="font-medium text-ink">{viewer.label}</span>」代理档（
      {formatDiscountLabel(viewer.ratio)}）：下方价格已按此折扣结算，并与划线原价对照。
    </div>
  )
}

/* ── 单行价格（逐分组）──────────────────────────────────── */

function PriceRow({
  price,
  listPrice,
  viewer,
  quotaPerYuan,
}: {
  price: PlazaPrice
  listPrice?: PlazaPrice
  viewer?: PlazaViewer
  quotaPerYuan: number
}) {
  const kind = billingKindOf(price)
  // 代理视图下 prices[].ratio 恒为 100（价格已折算过），真正生效的折扣在 viewer.ratio
  const ratio = viewer ? viewer.ratio : price.ratio
  // 价格生效时间：优先后端下发的 effective_at，其次 updated_at；两者都缺失则不展示
  // （后端 plazaPriceDTO 目前未下发，需补字段后此处自动生效）
  const effectiveAt = price.effective_at || price.updated_at || 0
  return (
    <tr>
      <td className="px-3 py-2.5 align-top">
        <div className="break-all font-mono text-ink-2">{price.group}</div>
        <div className="mt-0.5 font-mono text-[11px] text-ink-3">{ratioLabel(ratio)}</div>
        {effectiveAt > 0 && (
          <div className="mt-0.5 flex items-center gap-1 text-[11px] text-ink-3">
            <AppIcon name="clock" size={11} />
            价格生效 {formatDateTime(effectiveAt)}
          </div>
        )}
      </td>
      <td className="px-3 py-2.5 align-top">
        <Badge tone={kind === 'free' ? 'info' : 'brand'}>{billingKindLabel(kind)}</Badge>
      </td>
      <td className="px-3 py-2.5 text-right align-top">
        {viewer ? (
          <AgentPriceStack agentPrice={price} listPrice={listPrice} ratio={viewer.ratio} quotaPerYuan={quotaPerYuan} />
        ) : (
          <PriceLines price={price} quotaPerYuan={quotaPerYuan} className="items-end text-ink-2" />
        )}
      </td>
    </tr>
  )
}

/** 价格明细行：按计费方式给出可辨识的逐行文案（按量含输入/输出/缓存命中） */
function PriceLines({
  price,
  quotaPerYuan,
  className = '',
}: {
  price: PlazaPrice
  quotaPerYuan: number
  className?: string
}) {
  const kind = billingKindOf(price)
  const cache = cachePriceLabel(price, quotaPerYuan)
  if (kind === 'free') return <span className="text-ink-3">—</span>
  return (
    <div className={`flex flex-col gap-0.5 font-mono text-[12px] ${className}`}>
      {kind === 'per_call' ? (
        <span>{price.per_call_price > 0 ? formatYuanPerCall(price.per_call_price, quotaPerYuan) : '按次计费'}</span>
      ) : (
        <>
          <span>输入 {formatYuanPerMillion(price.prompt_price, quotaPerYuan)}</span>
          <span>输出 {formatYuanPerMillion(price.completion_price, quotaPerYuan)}</span>
          {/* 未配置缓存价时不渲染此行，避免「¥0.00/M」误导 */}
          {cache && <span className="text-ink-3">缓存命中 {cache}</span>}
        </>
      )}
    </div>
  )
}

/** 代理视图价格块：原价划线在上，橙色折扣块在下（沿用列表页的视觉语言） */
function AgentPriceStack({
  agentPrice,
  listPrice,
  ratio,
  quotaPerYuan,
}: {
  agentPrice: PlazaPrice
  listPrice?: PlazaPrice
  ratio: number
  quotaPerYuan: number
}) {
  return (
    <div className="flex flex-col items-end gap-1">
      {listPrice && (
        <PriceLines
          price={listPrice}
          quotaPerYuan={quotaPerYuan}
          className="items-end text-ink-3 line-through decoration-ink-3/70"
        />
      )}
      <div className="flex flex-col items-end gap-0.5 rounded border border-warn/40 bg-warn/15 px-2 py-1 font-mono text-[12px] font-medium text-warn">
        <PriceLines price={agentPrice} quotaPerYuan={quotaPerYuan} className="items-end" />
        <span className="text-[11px] font-normal opacity-80">{formatDiscountLabel(ratio)}</span>
      </div>
    </div>
  )
}
