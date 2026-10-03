/** 用户门户：任意门（/console/playground/anydoor）
 * 右侧栏为游戏/功能入口列表：长条卡片含标题、简介、状态、跳转按钮。
 * 数据由下方 GAMES 数组驱动，增改入口只需编辑该数组，页面打开即自动渲染。 */
'use client'

import Link from 'next/link'

import { Badge, Card } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'

/** 游戏/功能入口数据源：后续新增或调整入口，仅需修改此数组 */
interface GameEntry {
  title: string
  desc: string
  status: 'open' | 'maintenance'
  href: string
}

const GAMES: GameEntry[] = [
  {
    title: '多人推理',
    desc: '这是一个由 AI 制作的推理游戏，各位玩家可以绑定一个 APIKey 后 AI 将会制作一个推理剧情，你们中可能会有反派角色，你们需要投票选出反派角色。',
    status: 'open',
    href: '/console/playground/anydoor/findundercoveragent',
  },
  {
    title: 'AI 井字棋',
    desc: '与内置 AI 对手对弈井字棋，支持难度选择，适合休闲热身。',
    status: 'open',
    href: '/console/playground/tictactoe',
  },
  {
    title: '转盘抽奖',
    desc: '幸运大转盘，每日免费次数，奖品为平台额度与限定徽章。',
    status: 'open',
    href: '/console/playground/wheel',
  },
  {
    title: '见缝插针',
    desc: '经典反应小游戏，点击在旋转圆盘恰当时机插入针，考验手速。',
    status: 'maintenance',
    href: '/console/playground/pinch',
  },
  {
    title: 'Looptap',
    desc: '循环点击节奏游戏，跟随节拍连击，累计连击解锁成就。',
    status: 'maintenance',
    href: '/console/playground/looptap',
  },
  {
    title: '水果忍者',
    desc: '滑动切水果、避开炸弹，经典玩法适配键鼠与触屏。',
    status: 'open',
    href: '/console/playground/fruit',
  },
]

// 状态 → 徽章语义（复用项目既有 Badge tone，不引入新配色）
const STATUS_META: Record<GameEntry['status'], { tone: 'ok' | 'warn'; label: string }> = {
  open: { tone: 'ok', label: '开放中' },
  maintenance: { tone: 'warn', label: '维护中' },
}

export default function ConsoleAnyDoorPage() {
  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">任意门</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">游乐场下的功能入口，挑一个开始玩</p>
      </div>

      <div className="grid gap-5 lg:grid-cols-[1fr_300px]">
        {/* 左：任意门说明 */}
        <Card className="flex flex-col">
          <h2 className="text-[15px] font-semibold text-ink">关于任意门</h2>
          <p className="mt-2 text-[13px] leading-relaxed text-ink-3">
            任意门汇聚了站点内的各类休闲小游戏与互动功能。右侧每个入口是一张游戏卡片，
            标注了当前状态——「开放中」可立即进入，「维护中」暂不可玩。更多玩法会陆续接入。
          </p>
        </Card>

        {/* 右：游戏入口长条卡片列表（数据驱动，打开即渲染） */}
        <div className="space-y-3">
          {GAMES.map((game) => {
            const meta = STATUS_META[game.status]
            const playable = game.status === 'open'
            return (
              <Card key={game.href} className="rounded-lg">
                <div className="flex flex-col gap-2.5">
                  <div className="flex items-start justify-between gap-2">
                    <h3 className="text-[15px] font-semibold text-ink">{game.title}</h3>
                    <Badge tone={meta.tone}>{meta.label}</Badge>
                  </div>
                  <p className="line-clamp-2 text-[13px] leading-relaxed text-ink-3">{game.desc}</p>
                  <div className="pt-0.5">
                    {playable ? (
                      <Link href={game.href}>
                        <Button variant="primary" size="sm">
                          进入
                        </Button>
                      </Link>
                    ) : (
                      <Button variant="secondary" size="sm" disabled>
                        暂不可用
                      </Button>
                    )}
                  </div>
                </div>
              </Card>
            )
          })}
        </div>
      </div>
    </div>
  )
}
