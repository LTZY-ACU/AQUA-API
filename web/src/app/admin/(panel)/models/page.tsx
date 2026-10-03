/** 管理后台：模型管理（/admin/models）。
 *
 * 意图（Why）：
 *   本页承担两个职责，用 Tabs 分开（而非拆成两张页面——两者都回答"我有什么模型"）：
 *     1) 模型实体管理：把"渠道上的模型字符串"登记为一等实体（展示名/厂商/
 *        上下文长度/能力标签/启停），删除流程必须先拉取引用统计（/references），
 *        仍被令牌/渠道/计价/映射引用时红色警示并要求勾选确认——后端 DELETE
 *        刻意不做引用拦截（是否强删由管理员决定），"先看影响面"的防线在本页；
 *     2) 模型广场：原内嵌视图（PlazaEmbedded）原样保留，供查看对外呈现效果。
 *
 * 流转（Flow）：
 *   实体管理视图 → model-entities.ts（listModelEntities / create / update / delete
 *                  / fetchModelReferences）→ /api/admin/models*
 *   模型广场视图 → PlazaEmbedded → /api/models（公开数据，与从前一致）
 *
 * 扩展（Extend）：
 *   后端模型实体新增字段时：同步 model-entities.ts 的类型与 ModelFormModal 的表单；
 *   引用统计新增维度时：同步 DeleteModelModal 的统计清单与"仍被引用"判定。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import {
  createModelEntity,
  deleteModelEntity,
  fetchModelReferences,
  listModelEntities,
  updateModelEntity,
} from '@/api/model-entities'
import type { ModelEntity, ModelReferenceStats } from '@/api/model-entities'
import { PlazaEmbedded } from '@/components/plaza/PlazaEmbedded'
import { Badge, Card, Tabs } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch, Textarea } from '@/components/ui/Form'
import { Modal } from '@/components/ui/Modal'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime, formatNumber } from '@/utils/format'

/** 与后端 parsePagination 的 defaultPageSize 对齐，保证前后端翻页节奏一致 */
const PAGE_SIZE = 20

/** 页面视图：实体管理（默认，本页主体）/ 模型广场（原内嵌视图） */
type ModelsView = 'entities' | 'plaza'

/** 启用状态筛选值（'' = 全部，与后端"未给出则不过滤"的语义对齐） */
type EnabledFilter = '' | 'on' | 'off'

export default function AdminModelsPage() {
  const [view, setView] = useState<ModelsView>('entities')

  return (
    <div className="space-y-5">
      <Tabs
        items={[
          { value: 'entities', label: '实体管理' },
          { value: 'plaza', label: '模型广场' },
        ]}
        value={view}
        onChange={setView}
      />
      {view === 'entities' ? <ModelEntitiesPanel /> : <PlazaEmbedded />}
    </div>
  )
}

/* ── 实体管理面板 ───────────────────────────────────────── */

function ModelEntitiesPanel() {
  const { toastError } = useToast()

  const [items, setItems] = useState<ModelEntity[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)

  // 筛选草稿态：点「查询」才并入请求（与 logs 页同一模式，避免每敲一个字打一次后端）
  const [keyword, setKeyword] = useState('')
  const [vendor, setVendor] = useState('')
  const [enabledFilter, setEnabledFilter] = useState<EnabledFilter>('')

  // editing：null=关闭，'new'=新建，ModelEntity=编辑该实体
  const [editing, setEditing] = useState<ModelEntity | 'new' | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<ModelEntity | null>(null)
  // 正在就地切换启停的模型 id（0 = 无），用于给对应 Switch 置灰
  const [switchingId, setSwitchingId] = useState(0)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listModelEntities({
        page,
        size: PAGE_SIZE,
        keyword: keyword.trim() || undefined,
        vendor: vendor.trim() || undefined,
        enabled: enabledFilter === '' ? undefined : enabledFilter === 'on',
      })
      setItems(data.items)
      setTotal(data.total)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '模型列表加载失败')
    } finally {
      setLoading(false)
    }
  }, [page, keyword, vendor, enabledFilter, toastError])

  useEffect(() => {
    void load()
  }, [load])

  /** 查询：页码归 1；若已在第 1 页则直接刷新（useEffect 依赖 page，页号不变不触发） */
  function handleSearch() {
    setPage(1)
    if (page === 1) void load()
  }

  /** 重置：清空筛选并回到第 1 页 */
  function handleReset() {
    setKeyword('')
    setVendor('')
    setEnabledFilter('')
    setPage(1)
    if (page === 1) void load()
  }

  /** 列表内就地启停：只提交 enabled 一个字段（后端 PUT 支持部分更新） */
  async function handleToggle(model: ModelEntity) {
    setSwitchingId(model.id)
    try {
      await updateModelEntity(model.id, { enabled: !model.enabled })
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '启停更新失败')
    } finally {
      setSwitchingId(0)
    }
  }

  const columns: Column<ModelEntity>[] = [
    {
      title: '模型',
      render: (row) => (
        <div className="flex flex-col">
          <span className="font-medium text-ink">{row.label}</span>
          {/* 展示名与模型名不同才展示第二行，避免冗余 */}
          {row.display_name && row.display_name !== row.name ? (
            <span className="font-mono text-xs text-ink-3">{row.name}</span>
          ) : null}
        </div>
      ),
    },
    {
      title: '厂商',
      render: (row) => (row.vendor ? <Badge tone="info">{row.vendor}</Badge> : <span className="text-ink-3">—</span>),
    },
    {
      title: '上下文长度',
      align: 'right',
      render: (row) =>
        row.context_length > 0 ? (
          <span className="tabular-nums text-ink-2">{formatNumber(row.context_length)}</span>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
    {
      title: '能力标签',
      render: (row) => {
        if (!row.capabilities.length) return <span className="text-ink-3">—</span>
        return (
          <span className="flex flex-wrap gap-1">
            {row.capabilities.slice(0, 3).map((capability) => (
              <Badge key={capability} tone="off">{capability}</Badge>
            ))}
            {row.capabilities.length > 3 && (
              <span className="text-xs text-ink-3">+{row.capabilities.length - 3}</span>
            )}
          </span>
        )
      },
    },
    {
      title: '启用',
      align: 'center',
      render: (row) => (
        <div className="flex justify-center">
          <Switch
            checked={row.enabled}
            disabled={switchingId === row.id}
            onChange={() => handleToggle(row)}
            label={`启用模型 ${row.name}`}
          />
        </div>
      ),
    },
    {
      title: '更新时间',
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.updated_at)}</span>,
    },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setEditing(row)} className="text-ink-3 hover:text-brand">
            编辑
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">
            删除
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">模型实体</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">
            登记对外模型清单（展示名/厂商/上下文长度/能力标签），供广场展示与引用统计（共 {total} 个）
          </p>
        </div>
        <Button variant="primary" onClick={() => setEditing('new')}>
          新建模型
        </Button>
      </div>

      {/* 筛选：草稿态 + 查询按钮（回车同样触发） */}
      <Card>
        <div className="flex flex-wrap items-end gap-3">
          <Field label="关键词">
            <Input
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleSearch()}
              placeholder="模型名 / 展示名"
              className="w-44"
            />
          </Field>
          <Field label="厂商">
            <Input
              value={vendor}
              onChange={(e) => setVendor(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleSearch()}
              placeholder="精确匹配，如 deepseek"
              className="w-40"
            />
          </Field>
          <Field label="状态">
            <Select value={enabledFilter} onChange={(e) => setEnabledFilter(e.target.value as EnabledFilter)} className="w-32">
              <option value="">全部</option>
              <option value="on">启用</option>
              <option value="off">停用</option>
            </Select>
          </Field>
          <div className="flex items-center gap-2">
            <Button variant="primary" onClick={handleSearch}>查询</Button>
            <Button variant="secondary" onClick={handleReset}>重置</Button>
          </div>
        </div>
      </Card>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle="还没有模型实体"
          emptyDescription="新建模型实体，或从渠道编辑弹层里上游拉取模型后导入。"
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <ModelFormModal
        open={editing !== null}
        model={editing === 'new' || editing === null ? null : editing}
        onClose={() => setEditing(null)}
        onSaved={() => {
          setEditing(null)
          void load()
        }}
      />

      <DeleteModelModal
        model={deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onDeleted={() => {
          setDeleteTarget(null)
          void load()
        }}
      />
    </div>
  )
}

/* ── 新建 / 编辑弹层 ─────────────────────────────────────── */

function ModelFormModal({
  open,
  model,
  onClose,
  onSaved,
}: {
  open: boolean
  /** null = 新建；否则编辑该实体 */
  model: ModelEntity | null
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const [name, setName] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [vendor, setVendor] = useState('')
  const [contextLength, setContextLength] = useState('')
  const [capabilities, setCapabilities] = useState('')
  const [description, setDescription] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setName(model?.name ?? '')
    setDisplayName(model?.display_name ?? '')
    setVendor(model?.vendor ?? '')
    // 上下文长度 0 表示"未登记"，表单里以空字符串呈现而不是 0，避免误导
    setContextLength(model && model.context_length > 0 ? String(model.context_length) : '')
    setCapabilities(model?.capabilities.join(', ') ?? '')
    setDescription(model?.description ?? '')
    setEnabled(model?.enabled ?? true)
  }, [open, model])

  /** 能力标签解析：逗号（中英文）分隔 + 去空白 + 去空项，与后端归一化口径一致 */
  function parseCapabilities(text: string): string[] {
    return text
      .split(/[,，]/)
      .map((item) => item.trim())
      .filter(Boolean)
  }

  async function handleSubmit() {
    if (!model) {
      const trimmed = name.trim()
      if (!trimmed) {
        toastError('请填写模型名')
        return
      }
      // 与后端校验同口径：空白字符会让"看起来一样的名字"实际不相等，必须提前拦下
      if (/\s/.test(trimmed)) {
        toastError('模型名不能包含空白字符')
        return
      }
    }
    const ctx = contextLength.trim() === '' ? 0 : Number(contextLength)
    if (!Number.isInteger(ctx) || ctx < 0) {
      toastError('上下文长度需为非负整数')
      return
    }

    setLoading(true)
    try {
      // context_length/enabled/capabilities 总是提交（可清空/改 0）；
      // 文本字段提交 trim 后值（后端空字符串 = 保持原值，见 model-entities.ts 注释）
      const payload = {
        display_name: displayName.trim(),
        vendor: vendor.trim(),
        description: description.trim(),
        context_length: ctx,
        enabled,
        capabilities: parseCapabilities(capabilities),
      }
      if (model) {
        await updateModelEntity(model.id, payload)
        toast('模型已更新')
      } else {
        await createModelEntity({ ...payload, name: name.trim() })
        toast('模型已创建')
      }
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={model ? '编辑模型' : '新建模型'} width={560}>
      <div className="space-y-4">
        <Field
          label="模型名"
          required={!model}
          help={model ? '创建后不可修改：令牌白名单、渠道清单与映射都通过它关联，改名需新建后迁移引用' : '客户端请求时使用的名称；创建后不可修改，不能含空白字符'}
        >
          <Input value={name} onChange={(e) => setName(e.target.value)} disabled={Boolean(model)} placeholder="如 aqua-chat" />
        </Field>
        <Field label="展示名" help={model ? '留空提交将保持原值（后端空值视为不修改）' : '留空时界面回退显示模型名'}>
          <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="可选" />
        </Field>
        <Field label="厂商" help={model ? '留空提交将保持原值（后端空值视为不修改）' : '如 deepseek、openai；最长 64 字符'}>
          <Input value={vendor} onChange={(e) => setVendor(e.target.value)} placeholder="可选" />
        </Field>
        <Field label="上下文长度" help="单位 token；留空或 0 表示未登记">
          <Input
            value={contextLength}
            onChange={(e) => setContextLength(e.target.value)}
            type="number"
            min={0}
            placeholder="如 128000"
          />
        </Field>
        <Field label="能力标签" help="逗号分隔，如 chat, stream, tools；清空后保存可移除全部标签">
          <Input value={capabilities} onChange={(e) => setCapabilities(e.target.value)} placeholder="chat, stream, tools" />
        </Field>
        <Field label="说明">
          <Textarea value={description} onChange={(e) => setDescription(e.target.value)} rows={3} placeholder="可选，展示给用户看的模型简介" />
        </Field>
        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>{model ? '启用该模型' : '创建后立即启用'}</span>
          <Switch checked={enabled} onChange={setEnabled} label={model ? '启用该模型' : '创建后立即启用'} />
        </label>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>取消</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{model ? '保存' : '创建'}</Button>
      </div>
    </Modal>
  )
}

/* ── 删除弹层（先看引用统计，再决定） ────────────────────── */

function DeleteModelModal({
  model,
  onClose,
  onDeleted,
}: {
  /** null = 关闭；否则对它走"统计 → 警示 → 删除"流程 */
  model: ModelEntity | null
  onClose: () => void
  onDeleted: () => void
}) {
  const { toast, toastError } = useToast()
  const [stats, setStats] = useState<ModelReferenceStats | null>(null)
  const [statsFailed, setStatsFailed] = useState(false)
  const [acknowledged, setAcknowledged] = useState(false)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!model) return
    // 每次打开都重新统计：引用情况随时在变，拿旧数据做删除判断会漏报
    setStats(null)
    setStatsFailed(false)
    setAcknowledged(false)
    let cancelled = false
    fetchModelReferences(model.name)
      .then((data) => {
        if (!cancelled) setStats(data)
      })
      .catch(() => {
        if (!cancelled) setStatsFailed(true)
      })
    return () => {
      cancelled = true
    }
  }, [model])

  /** 是否仍被引用：四个维度任一非零都算（group_count 不在后端 total 里，但同样算影响面） */
  const referenced = Boolean(
    stats && stats.token_count + stats.channel_count + stats.group_count + stats.mapping_count > 0,
  )

  /** 统计没拿到（加载中或失败）就不允许删：看不清影响面时的删除等于盲删 */
  const canDelete = Boolean(stats) && !statsFailed && (!referenced || acknowledged)

  async function handleDelete() {
    if (!model) return
    setLoading(true)
    try {
      await deleteModelEntity(model.id)
      toast('模型已删除')
      onDeleted()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '删除失败')
    } finally {
      setLoading(false)
    }
  }

  const statRows: { label: string; value: number; hint: string }[] = stats
    ? [
        { label: '令牌白名单', value: stats.token_count, hint: '白名单包含该模型的令牌数' },
        { label: '渠道清单', value: stats.channel_count, hint: '模型清单包含该模型的渠道数' },
        { label: '计价分组', value: stats.group_count, hint: '计价规则引用该模型的分组数' },
        { label: '渠道映射', value: stats.mapping_count, hint: '映射中涉及该模型的条数' },
      ]
    : []

  return (
    <Modal open={Boolean(model)} onClose={onClose} title="删除模型" width={520}>
      {model && (
        <div className="space-y-4">
          <div className="text-sm text-ink-2">
            确认删除 <span className="font-mono font-medium text-ink">{model.name}</span>？
          </div>

          {/* 引用统计区：拿不到就禁止删除（红色警示），拿到则逐项展示 */}
          {statsFailed ? (
            <div className="rounded-md border border-err/30 bg-err/10 px-3 py-2.5 text-[13px] text-err">
              引用统计获取失败，无法评估删除影响，已禁止删除。请稍后重试或检查后端服务。
            </div>
          ) : stats ? (
            <div className="space-y-2">
              <div className="text-[13px] font-medium text-ink-2">删除前引用统计</div>
              <div className="divide-y divide-line rounded-md border border-line">
                {statRows.map((row) => (
                  <div key={row.label} className="flex items-center justify-between px-3 py-2">
                    <span className="text-[13px] text-ink-2">
                      {row.label}
                      <span className="ml-2 text-xs text-ink-3">{row.hint}</span>
                    </span>
                    <Badge tone={row.value > 0 ? 'warn' : 'off'}>{row.value}</Badge>
                  </div>
                ))}
              </div>
              {referenced ? (
                <div className="space-y-2.5 rounded-md border border-err/30 bg-err/10 px-3 py-2.5">
                  <div className="text-[13px] font-medium text-err">该模型仍被上述配置引用</div>
                  <div className="text-xs leading-relaxed text-err/90">
                    删除后：引用它的令牌与渠道请求将无法命中该模型（线上 404），计价规则失去目标。
                    建议先到渠道 / 令牌 / 价格页移除引用后再删除。
                  </div>
                  <label className="flex items-start gap-2 text-[13px] text-ink-2">
                    <input
                      type="checkbox"
                      checked={acknowledged}
                      onChange={(e) => setAcknowledged(e.target.checked)}
                      className="mt-0.5 h-4 w-4 accent-err"
                    />
                    我已知晓删除影响，仍要删除
                  </label>
                </div>
              ) : (
                <div className="rounded-md border border-ok/30 bg-ok/10 px-3 py-2 text-[13px] text-ok">
                  未被任何令牌、渠道、计价与映射引用，可安全删除。
                </div>
              )}
            </div>
          ) : (
            <div className="text-[13px] text-ink-3">正在统计该模型被哪些配置引用…</div>
          )}

          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>取消</Button>
            <Button variant="danger" loading={loading} disabled={!canDelete} onClick={handleDelete}>
              {referenced ? '仍要删除' : '删除'}
            </Button>
          </div>
        </div>
      )}
    </Modal>
  )
}
