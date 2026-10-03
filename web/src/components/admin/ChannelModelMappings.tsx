/** 渠道模型映射维护分区（嵌在渠道编辑弹层下方）。
 *
 * 意图（Why）：
 *   /admin/model-mappings 总览页明确注释「映射在渠道详情中维护」，但渠道编辑表单
 *   从未提供该入口——设计与实现脱节，映射数据此前只能靠直接调 API 写入。
 *   映射解决的是「用户侧模型名 ≠ 上游模型名」的改写问题（对外叫 gpt-4o、
 *   上游实际叫 azure-gpt-4o-2024 之类），多渠道聚合时几乎必然用到。
 *
 *   保存按钮与弹层主表单（渠道字段）彼此独立：改映射不等于改渠道配置，
 *   也不应该要求「先保存表单才能保存映射」——两者提交到不同的接口。
 *
 * 流转（Flow）：
 *   渠道编辑弹层下方分区 → <ChannelModelMappings channelId/>
 *   → listChannelMappings / replaceChannelMappings → GET/PUT /api/admin/channels/:id/mappings
 *   PUT 是整组替换：界面上删掉的行保存后会被真的删除（后端保存后即清除转发层缓存）。
 *
 * 扩展（Extend）：
 *   行内暂不提供 priority / remark 的编辑位（弹层列宽预算；priority 仅在多个渠道
 *   映射同一模型 ID 竞争时才需要调整）——但保存时会原样回传已有值，避免整组替换
 *   把它们抹掉。需要精细编辑时给表格加列即可，行模型已携带这两个字段。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { listChannelMappings, replaceChannelMappings } from '@/api/admin'
import type { ChannelModelMappingItem } from '@/api/types'
import { SkeletonRows } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Input, Switch } from '@/components/ui/Form'
import { useToast } from '@/lib/toast/toast-context'

interface ChannelModelMappingsProps {
  /** 渠道 ID；变化时重新拉取该渠道的映射 */
  channelId: number
}

/** 编辑中的一行映射。 */
interface MappingRow {
  key: number
  publicModel: string
  upstreamModel: string
  enabled: boolean
  // priority / remark 无 UI 编辑位（见文件头注释），仅作「保存时不丢已有值」的透传载体
  priority: number
  remark: string
}

/** 新行 key 用负数自减：与库中条目的自增 id 空间不冲突，增删行时 React key 保持稳定。 */
let nextRowKey = -1

function emptyMappingRow(): MappingRow {
  return { key: nextRowKey--, publicModel: '', upstreamModel: '', enabled: true, priority: 0, remark: '' }
}

export function ChannelModelMappings({ channelId }: ChannelModelMappingsProps) {
  const { toast, toastError } = useToast()
  const [rows, setRows] = useState<MappingRow[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listChannelMappings(channelId)
      setRows(
        (data.items ?? []).map((m) => ({
          key: m.id,
          publicModel: m.public_model,
          upstreamModel: m.upstream_model,
          enabled: m.enabled,
          priority: m.priority,
          remark: m.remark,
        })),
      )
    } catch (err) {
      toastError(err instanceof Error ? err.message : '模型映射加载失败')
    } finally {
      setLoading(false)
    }
  }, [channelId, toastError])

  useEffect(() => {
    void load()
  }, [load])

  function updateRow(key: number, patch: Partial<MappingRow>) {
    setRows((prev) => prev.map((row) => (row.key === key ? { ...row, ...patch } : row)))
  }

  function removeRow(key: number) {
    setRows((prev) => prev.filter((row) => row.key !== key))
  }

  async function handleSave() {
    const items: ChannelModelMappingItem[] = []
    for (const row of rows) {
      const publicModel = row.publicModel.trim()
      const upstreamModel = row.upstreamModel.trim()
      // 两侧全空的行视为草稿，直接跳过（保存后自然消失）
      if (!publicModel && !upstreamModel) continue
      // 只填一侧的映射既无法匹配请求也无法改写，保存只会得到一条永远不生效的规则
      if (!publicModel || !upstreamModel) {
        toastError('存在只填了一侧的映射行，请补全两侧或删除该行')
        return
      }
      items.push({
        public_model: publicModel,
        upstream_model: upstreamModel,
        enabled: row.enabled,
        priority: row.priority,
        remark: row.remark,
      })
    }
    setSaving(true)
    try {
      const data = await replaceChannelMappings(channelId, items)
      // 用保存响应回填：整组替换语义下保证「界面所见 = 落库结果」
      setRows(
        (data.items ?? []).map((m) => ({
          key: m.id,
          publicModel: m.public_model,
          upstreamModel: m.upstream_model,
          enabled: m.enabled,
          priority: m.priority,
          remark: m.remark,
        })),
      )
      toast(`映射已保存（${data.total} 条）`)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-[13px] font-semibold text-ink">模型映射</h3>
          <p className="mt-0.5 text-xs text-ink-3">
            平台模型 ID（用户调用）↔ 上游模型 ID（实际转发）；不配置则原样透传，两侧都支持尾部通配符 *。
            此处保存独立于上方渠道表单。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" size="sm" onClick={() => setRows((prev) => [...prev, emptyMappingRow()])}>
            新增一行
          </Button>
          <Button variant="primary" size="sm" loading={saving} onClick={handleSave}>
            保存映射
          </Button>
        </div>
      </div>

      {loading ? (
        <SkeletonRows rows={2} />
      ) : (
        <div className="overflow-hidden rounded-md border border-line">
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-[13px]">
              <thead>
                <tr className="border-b border-line bg-surface/70 text-ink-3">
                  <th className="px-2 py-2 text-left font-medium">平台模型 ID</th>
                  <th className="px-2 py-2 text-left font-medium">上游模型 ID</th>
                  <th className="px-2 py-2 text-center font-medium">启用</th>
                  <th className="px-2 py-2 text-right font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.key} className="border-b border-line/70 last:border-0">
                    <td className="px-2 py-1.5">
                      <Input
                        value={row.publicModel}
                        onChange={(e) => updateRow(row.key, { publicModel: e.target.value })}
                        placeholder="如 gpt-4o 或 gpt-4*"
                      />
                    </td>
                    <td className="px-2 py-1.5">
                      <Input
                        value={row.upstreamModel}
                        onChange={(e) => updateRow(row.key, { upstreamModel: e.target.value })}
                        placeholder="实际发给上游的名字"
                      />
                    </td>
                    <td className="px-2 py-1.5 text-center">
                      <Switch
                        checked={row.enabled}
                        onChange={(v) => updateRow(row.key, { enabled: v })}
                        label="启用该映射"
                      />
                    </td>
                    <td className="px-2 py-1.5 text-right">
                      <button
                        type="button"
                        onClick={() => removeRow(row.key)}
                        className="text-[13px] text-ink-3 hover:text-err"
                      >
                        删除
                      </button>
                    </td>
                  </tr>
                ))}
                {rows.length === 0 && (
                  <tr>
                    <td colSpan={4} className="px-3 py-5 text-center text-ink-3">
                      未配置映射：该渠道的模型名将原样透传给上游
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}
