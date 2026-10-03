/** 管理后台：群发邮件（/admin/broadcast）。
 *
 * 意图（Why）：
 *   后端把群发设计成「模板目录 → 预览发给自己 → 显式确认」三道闸门（handler_broadcast.go），
 *   发出即到达、不可撤回，因此本页是向导式流程而非一张普通表单：
 *   选模板 → 预览（发到管理员自己邮箱核对排版）→ 摘要复核 + 二次确认弹窗后才真正群发，
 *   全程不提供跳过预览的捷径。
 *
 * 流转（Flow）：
 *   发起向导 BroadcastWizardModal → listBroadcastTemplates → previewBroadcast
 *                                 → createBroadcast（confirm=true，经 ConfirmDialog）
 *   历史列表 load() → listBroadcasts → 表格 + 分页；
 *   存在进行中批次时每 5 秒静默轮询进度（不打断阅读）
 *   收件明细 RecipientsModal → listBroadcastRecipients（状态筛选 + 分页）
 *   停止 → ConfirmDialog → cancelBroadcast（未发的不发，已发的不撤回）
 *
 * 扩展（Extend）：
 *   后端新增批次/收件人状态：同步 broadcast.ts 的类型与下方 STATUS_META / RECIPIENT_META；
 *   后端新增通知模板会自动出现在向导第一步（模板目录是动态下发的，前端零改动）。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import {
  cancelBroadcast,
  createBroadcast,
  listBroadcastRecipients,
  listBroadcasts,
  listBroadcastTemplates,
  previewBroadcast,
  type BroadcastRecipient,
  type BroadcastStatus,
  type BroadcastTemplate,
  type EmailBroadcast,
  type RecipientStatus,
} from '@/api/broadcast'
import { Badge, Card, EmptyState, SkeletonRows, Tabs } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

const PAGE_SIZE = 20

/** 进行中批次的进度轮询间隔（毫秒）：够快能盯到推进，够慢不给后端添压 */
const POLL_INTERVAL_MS = 5000

/** 批次状态 → 徽标配色与文案：发送中用品牌色高亮，扫视时能立刻定位要盯进度的行 */
const STATUS_META: Record<BroadcastStatus, { tone: 'info' | 'brand' | 'ok' | 'warn'; text: string }> = {
  pending: { tone: 'info', text: '排队中' },
  running: { tone: 'brand', text: '发送中' },
  done: { tone: 'ok', text: '已完成' },
  canceled: { tone: 'warn', text: '已停止' },
}

/** 收件人投递状态 → 徽标配色与文案：失败用红色，核对失败明细时最醒目 */
const RECIPIENT_META: Record<RecipientStatus, { tone: 'ok' | 'err' | 'off'; text: string }> = {
  sent: { tone: 'ok', text: '已发送' },
  failed: { tone: 'err', text: '失败' },
  pending: { tone: 'off', text: '待发送' },
}

/** 向导三步的标题（步骤指示条用） */
const WIZARD_STEPS = ['选择模板', '预览确认', '正式发送'] as const

/** 是否还有批次在发送（决定是否轮询刷新进度） */
function isActiveBroadcast(bc: EmailBroadcast): boolean {
  return bc.status === 'pending' || bc.status === 'running'
}

export default function AdminBroadcastPage() {
  const [items, setItems] = useState<EmailBroadcast[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [wizardOpen, setWizardOpen] = useState(false)
  const [detail, setDetail] = useState<EmailBroadcast | null>(null)
  const [cancelTarget, setCancelTarget] = useState<EmailBroadcast | null>(null)
  const [cancelLoading, setCancelLoading] = useState(false)
  const { toast, toastError } = useToast()

  /**
   * 加载批次列表。
   * silent=true 供轮询使用：不把表格切回骨架屏，数据原地更新不打断阅读。
   */
  const load = useCallback(
    async (silent = false) => {
      if (!silent) setLoading(true)
      try {
        const data = await listBroadcasts({ page, size: PAGE_SIZE })
        setItems(data.items)
        setTotal(data.total)
      } catch {
        /* 401 统一处理；列表加载失败保持现状 */
      } finally {
        if (!silent) setLoading(false)
      }
    },
    [page],
  )

  useEffect(() => {
    void load()
  }, [load])

  // 有进行中的批次才轮询：全部结束后自动停表，不为静态数据持续发请求
  const hasActive = items.some(isActiveBroadcast)
  useEffect(() => {
    if (!hasActive) return
    const timer = setInterval(() => void load(true), POLL_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [hasActive, load])

  async function handleCancel() {
    if (!cancelTarget) return
    setCancelLoading(true)
    try {
      const updated = await cancelBroadcast(cancelTarget.id)
      toast(`已停止群发「${updated.subject}」：已发 ${updated.sent} 封，剩余 ${updated.pending} 封不再发送`)
      setCancelTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '停止群发失败')
    } finally {
      setCancelLoading(false)
    }
  }

  const columns: Column<EmailBroadcast>[] = [
    { title: 'ID', width: 'w-16', render: (row) => <span className="text-ink-3">#{row.id}</span> },
    {
      title: '主题',
      render: (row) => (
        <span className="block min-w-0">
          <span className="block truncate font-medium text-ink">{row.subject}</span>
          <span className="block text-xs text-ink-3">模板 {row.template}</span>
        </span>
      ),
    },
    { title: '状态', render: (row) => <Badge tone={STATUS_META[row.status].tone}>{STATUS_META[row.status].text}</Badge> },
    {
      title: '成功/失败/待发',
      render: (row) => (
        <span className="text-[13px] tabular-nums">
          <span className="text-ok">{row.sent}</span>
          <span className="text-ink-3"> / </span>
          <span className={row.failed > 0 ? 'font-medium text-err' : 'text-ink-3'}>{row.failed}</span>
          <span className="text-ink-3"> / </span>
          <span className="text-ink-2">{row.pending}</span>
          <span className="ml-1.5 text-ink-3">共 {row.total}</span>
        </span>
      ),
    },
    {
      title: '发起时间',
      render: (row) => <span className="text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
    },
    {
      title: '结束时间',
      render: (row) => (
        <span className="text-[13px] text-ink-3">{row.finished_at > 0 ? formatDateTime(row.finished_at) : '—'}</span>
      ),
    },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setDetail(row)} className="text-ink-3 hover:text-brand">
            明细
          </button>
          {isActiveBroadcast(row) && (
            <button type="button" onClick={() => setCancelTarget(row)} className="text-ink-3 hover:text-warn">
              停止
            </button>
          )}
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 text-xl font-bold text-ink">
            群发邮件
            {hasActive && <Badge tone="brand">进度自动刷新中</Badge>}
          </h1>
          <p className="mt-0.5 text-[13px] text-ink-3">
            向全体用户发送通知邮件：先预览发给自己，确认无误再群发（共 {total} 批）
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={() => void load()}>
            刷新
          </Button>
          <Button variant="primary" onClick={() => setWizardOpen(true)}>
            发起群发
          </Button>
        </div>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle="还没有群发记录"
          emptyDescription="点「发起群发」，按向导先预览再确认发送"
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <BroadcastWizardModal
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        onCreated={() => {
          // 新批次排在列表第 1 页（后端按创建时间倒序）：翻回第 1 页并立即刷新
          setPage(1)
          void load()
        }}
      />

      {/* 条件挂载：每次打开都是全新实例，页码/筛选天然重置，避免上次核对状态残留 */}
      {detail && <BroadcastRecipientsModal broadcast={detail} onClose={() => setDetail(null)} />}

      <ConfirmDialog
        open={Boolean(cancelTarget)}
        title="停止群发"
        message={`确认停止群发「${cancelTarget?.subject}」？停止后未发出的 ${cancelTarget?.pending ?? 0} 封将不再发送，已发出的无法撤回。`}
        danger
        confirmText="停止发送"
        loading={cancelLoading}
        onConfirm={handleCancel}
        onCancel={() => setCancelTarget(null)}
      />
    </div>
  )
}

/* ── 向导步骤指示条：当前步高亮，已完成打勾 ─────────────── */

function WizardSteps({ current }: { current: number }) {
  return (
    <div className="flex items-center gap-2">
      {WIZARD_STEPS.map((label, index) => {
        const step = index + 1
        const state = step < current ? 'done' : step === current ? 'active' : 'todo'
        return (
          <div key={label} className="flex items-center gap-2">
            {index > 0 && <span className="h-px w-6 bg-line-2" aria-hidden="true" />}
            <span
              className={`flex h-6 w-6 items-center justify-center rounded-full text-xs font-medium ${
                state === 'active'
                  ? 'bg-brand text-on-brand'
                  : state === 'done'
                    ? 'bg-brand/10 text-brand'
                    : 'bg-ink/5 text-ink-3'
              }`}
            >
              {state === 'done' ? '✓' : step}
            </span>
            <span className={`text-[13px] ${state === 'active' ? 'font-medium text-ink' : 'text-ink-3'}`}>{label}</span>
          </div>
        )
      })}
    </div>
  )
}

/* ── 发起群发向导：选模板 → 预览发给自己 → 摘要 + 二次确认 ── */

function BroadcastWizardModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const { toast, toastError } = useToast()
  const [step, setStep] = useState(1)
  const [templates, setTemplates] = useState<BroadcastTemplate[]>([])
  const [templatesLoading, setTemplatesLoading] = useState(false)
  const [selected, setSelected] = useState('')
  const [previewing, setPreviewing] = useState(false)
  const [preview, setPreview] = useState<{ subject: string; to: string } | null>(null)
  const [creating, setCreating] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 打开时重置向导并拉取模板目录：模板清单由后端下发，前端不写死
  useEffect(() => {
    if (!open) return
    setStep(1)
    setSelected('')
    setPreview(null)
    setConfirmOpen(false)
    setTemplatesLoading(true)
    listBroadcastTemplates()
      .then((data) => setTemplates(data.items))
      .catch((err) => toastError(err instanceof Error ? err.message : '模板目录加载失败'))
      .finally(() => setTemplatesLoading(false))
  }, [open])

  const selectedLabel = templates.find((tpl) => tpl.key === selected)?.label ?? selected

  async function handlePreview() {
    setPreviewing(true)
    try {
      const result = await previewBroadcast(selected)
      setPreview(result)
      toast(`预览邮件已发送到 ${result.to}，请去收件箱确认排版`)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '预览发送失败')
    } finally {
      setPreviewing(false)
    }
  }

  async function handleCreate() {
    setCreating(true)
    try {
      const bc = await createBroadcast({ template: selected, confirm: true })
      toast(`群发已开始：已入队 ${bc.total} 位收件人`)
      setConfirmOpen(false)
      onCreated()
      onClose()
    } catch (err) {
      // 弹窗保持打开：后端 message 会说明失败原因（如未配置邮件通道），允许修正后重试
      toastError(err instanceof Error ? err.message : '群发发起失败')
    } finally {
      setCreating(false)
    }
  }

  /** 发送规则的静态说明（第 2 步用）：集中一处定义，避免口径漂移 */
  const rules = (
    <ul className="mt-1.5 list-disc space-y-1 pl-4 leading-relaxed">
      <li>收件人：全部「启用」状态且已填写邮箱的用户（发送时按最新名单入队）。</li>
      <li>主题与正文按当前站点设置渲染后快照落库，模板后续改动不影响本次发送。</li>
      <li>邮件一经发出不可撤回；发送中可随时停止，未发出的不再发。</li>
    </ul>
  )

  return (
    <>
      <Modal open={open} onClose={onClose} title="发起群发" width={560}>
        <div className="space-y-5">
          <WizardSteps current={step} />

          {step === 1 && (
            <div className="space-y-3">
              {templatesLoading ? (
                <SkeletonRows rows={3} />
              ) : templates.length === 0 ? (
                <EmptyState
                  title="暂无可用通知模板"
                  description="通知模板由后端统一定义，当前没有可选模板"
                />
              ) : (
                <div className="space-y-2">
                  {templates.map((tpl) => {
                    const active = selected === tpl.key
                    return (
                      <button
                        key={tpl.key}
                        type="button"
                        onClick={() => {
                          setSelected(tpl.key)
                          // 换了模板就作废旧预览：预览过的是上一个模板，不能作为"已核对"的依据
                          setPreview(null)
                        }}
                        className={`flex w-full items-center gap-3 rounded-lg border p-3 text-left transition ${
                          active
                            ? 'border-brand bg-brand/5'
                            : 'border-line bg-card hover:border-line-2 hover:bg-surface/50'
                        }`}
                      >
                        <span
                          className={`flex h-4 w-4 shrink-0 items-center justify-center rounded-full border ${
                            active ? 'border-brand' : 'border-line-2'
                          }`}
                          aria-hidden="true"
                        >
                          {active && <span className="h-2 w-2 rounded-full bg-brand" />}
                        </span>
                        <span className="min-w-0">
                          <span className="block text-sm font-medium text-ink">{tpl.label}</span>
                          <span className="block text-xs text-ink-3">{tpl.key}</span>
                        </span>
                      </button>
                    )
                  })}
                </div>
              )}
            </div>
          )}

          {step === 2 && (
            <div className="space-y-4">
              <div className="rounded-lg border border-line bg-surface/50 p-4 text-[13px] text-ink-2">
                <div className="font-medium text-ink">发送说明</div>
                {rules}
              </div>
              <div className="text-sm text-ink-2">
                已选模板：<span className="font-medium text-ink">{selectedLabel}</span>
              </div>
              <div>
                <Button variant="secondary" loading={previewing} onClick={handlePreview}>
                  预览：发送到我自己的邮箱
                </Button>
              </div>
              {preview && (
                <div className="rounded-lg border border-ok/25 bg-ok/5 p-3 text-[13px] text-ink-2">
                  <div className="font-medium text-ok">预览邮件已发出</div>
                  <div className="mt-1">收件人：{preview.to}</div>
                  <div>主题：{preview.subject}</div>
                  <div className="mt-1 text-xs text-ink-3">
                    请到收件箱确认排版无误后再进入下一步；正式群发的内容与预览完全一致。
                  </div>
                </div>
              )}
              {!preview && (
                <div className="text-xs text-ink-3">完成预览后才能进入下一步——群发前必须先亲眼看过内容。</div>
              )}
            </div>
          )}

          {step === 3 && (
            <div className="space-y-4">
              <div className="rounded-lg border border-err/25 bg-err/5 p-4 text-[13px] text-ink-2">
                <div className="font-medium text-err">即将全站群发，请最后核对</div>
                <ul className="mt-1.5 list-disc space-y-1 pl-4 leading-relaxed">
                  <li>
                    模板：{selectedLabel}（{selected}）
                  </li>
                  <li>主题：{preview?.subject ?? '—'}</li>
                  <li>收件人：全部启用且已填写邮箱的用户，人数在发送时按最新名单确定</li>
                  <li>预览收件箱：{preview?.to ?? '—'}</li>
                </ul>
              </div>
              <div className="text-xs text-ink-3">点击「确认群发」后，还需在弹窗中再次确认才会真正发送。</div>
            </div>
          )}
        </div>

        <div className="mt-5 flex justify-end gap-2">
          {step > 1 && (
            <Button variant="ghost" onClick={() => setStep(step - 1)}>
              上一步
            </Button>
          )}
          {step < 3 ? (
            <Button
              variant="primary"
              disabled={step === 1 ? !selected : !preview}
              onClick={() => setStep(step + 1)}
            >
              下一步
            </Button>
          ) : (
            <Button variant="danger" onClick={() => setConfirmOpen(true)}>
              确认群发
            </Button>
          )}
        </div>
      </Modal>

      {/* 最终闸门：与后端 confirm 字段对应的显式二次确认 */}
      <ConfirmDialog
        open={confirmOpen}
        title="确认全站群发"
        message={`确认向全部启用且已填写邮箱的用户发送「${preview?.subject ?? selectedLabel}」？邮件一经发出不可撤回，重复发送会引发投诉。`}
        danger
        confirmText="确认发送"
        loading={creating}
        onConfirm={handleCreate}
        onCancel={() => setConfirmOpen(false)}
      />
    </>
  )
}

/* ── 收件人明细弹层：状态筛选 + 分页（失败核对靠它） ────── */

function BroadcastRecipientsModal({
  broadcast,
  onClose,
}: {
  broadcast: EmailBroadcast
  onClose: () => void
}) {
  const [items, setItems] = useState<BroadcastRecipient[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState<'' | RecipientStatus>('')
  const [loading, setLoading] = useState(true)
  const { toastError } = useToast()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listBroadcastRecipients(broadcast.id, {
        page,
        size: PAGE_SIZE,
        status: status || undefined,
      })
      setItems(data.items)
      setTotal(data.total)
    } catch (err) {
      // 明细查询失败必须出声：空表会被误读成"没人失败"，掩盖真实故障
      toastError(err instanceof Error ? err.message : '查询收件人明细失败')
    } finally {
      setLoading(false)
    }
  }, [broadcast, page, status, toastError])

  useEffect(() => {
    void load()
  }, [load])

  const columns: Column<BroadcastRecipient>[] = [
    { title: '用户', width: 'w-16', render: (row) => <span className="text-ink-3">#{row.user_id}</span> },
    { title: '邮箱', render: (row) => <span className="break-all text-ink">{row.email}</span> },
    { title: '状态', render: (row) => <Badge tone={RECIPIENT_META[row.status].tone}>{RECIPIENT_META[row.status].text}</Badge> },
    {
      title: '失败原因',
      render: (row) =>
        row.error ? (
          <span className="break-all text-[13px] text-err" title={row.error}>
            {row.error}
          </span>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
    {
      title: '发送时间',
      render: (row) => <span className="text-[13px] text-ink-3">{row.sent_at > 0 ? formatDateTime(row.sent_at) : '—'}</span>,
    },
  ]

  return (
    <Modal open onClose={onClose} title="收件人明细" width={720}>
      <div className="space-y-4">
        {/* 批次概要：不依赖父级轮询数据，展示打开时的快照；发送中的批次可用「刷新」看到最新明细 */}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate text-sm font-medium text-ink">{broadcast.subject}</span>
              <Badge tone={STATUS_META[broadcast.status].tone}>{STATUS_META[broadcast.status].text}</Badge>
            </div>
            <div className="mt-1 text-xs text-ink-3">
              #{broadcast.id} · 模板 {broadcast.template} · 成功 {broadcast.sent} · 失败 {broadcast.failed} · 待发{' '}
              {broadcast.pending}
            </div>
          </div>
          <Button variant="secondary" size="sm" onClick={() => void load()}>
            刷新
          </Button>
        </div>

        <Tabs
          items={[
            { value: '', label: '全部' },
            { value: 'sent', label: '已发送' },
            { value: 'failed', label: '失败' },
            { value: 'pending', label: '待发送' },
          ]}
          value={status}
          onChange={(value) => {
            setStatus(value)
            setPage(1)
          }}
        />

        <div>
          <DataTable
            columns={columns}
            rows={loading ? null : items}
            loading={loading}
            rowKey={(row) => row.id}
            emptyTitle="没有符合条件的收件人"
            emptyDescription="切换状态筛选，或稍后刷新——进行中的批次发送完成后状态会更新"
          />
          <div className="pt-3">
            <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
          </div>
        </div>
      </div>
    </Modal>
  )
}
