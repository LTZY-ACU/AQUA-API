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
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

const PAGE_SIZE = 20

/** 进行中批次的进度轮询间隔（毫秒）：够快能盯到推进，够慢不给后端添压 */
const POLL_INTERVAL_MS = 5000

/** 批次状态 → 徽标配色与词条键：发送中用品牌色高亮，扫视时能立刻定位要盯进度的行 */
const STATUS_META: Record<BroadcastStatus, { tone: 'info' | 'brand' | 'ok' | 'warn'; key: string }> = {
  pending: { tone: 'info', key: 'admin.broadcast.status.pending' },
  running: { tone: 'brand', key: 'admin.broadcast.status.running' },
  done: { tone: 'ok', key: 'admin.broadcast.status.done' },
  canceled: { tone: 'warn', key: 'admin.broadcast.status.canceled' },
}

/** 收件人投递状态 → 徽标配色与词条键：失败用红色，核对失败明细时最醒目 */
const RECIPIENT_META: Record<RecipientStatus, { tone: 'ok' | 'err' | 'off'; key: string }> = {
  sent: { tone: 'ok', key: 'admin.broadcast.recipientStatus.sent' },
  failed: { tone: 'err', key: 'admin.broadcast.recipientStatus.failed' },
  pending: { tone: 'off', key: 'admin.broadcast.recipientStatus.pending' },
}

/** 向导三步的标题词条键（步骤指示条用） */
const WIZARD_STEP_KEYS = [
  'admin.broadcast.wizard.step1',
  'admin.broadcast.wizard.step2',
  'admin.broadcast.wizard.step3',
] as const

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
  const { t } = useI18n()

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
      toast(t('admin.broadcast.toast.canceled', { subject: updated.subject, sent: updated.sent, pending: updated.pending }))
      setCancelTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.broadcast.toast.cancelFailed'))
    } finally {
      setCancelLoading(false)
    }
  }

  const columns: Column<EmailBroadcast>[] = [
    { title: 'ID', width: 'w-16', render: (row) => <span className="text-ink-3">#{row.id}</span> },
    {
      title: t('admin.broadcast.col.subject'),
      render: (row) => (
        <span className="block min-w-0">
          <span className="block truncate font-medium text-ink">{row.subject}</span>
          <span className="block text-xs text-ink-3">{t('admin.broadcast.templateTag', { template: row.template })}</span>
        </span>
      ),
    },
    { title: t('admin.broadcast.col.status'), render: (row) => <Badge tone={STATUS_META[row.status].tone}>{t(STATUS_META[row.status].key)}</Badge> },
    {
      title: t('admin.broadcast.col.counts'),
      render: (row) => (
        <span className="text-[13px] tabular-nums">
          <span className="text-ok">{row.sent}</span>
          <span className="text-ink-3"> / </span>
          <span className={row.failed > 0 ? 'font-medium text-err' : 'text-ink-3'}>{row.failed}</span>
          <span className="text-ink-3"> / </span>
          <span className="text-ink-2">{row.pending}</span>
          <span className="ml-1.5 text-ink-3">{t('admin.broadcast.totalTag', { total: row.total })}</span>
        </span>
      ),
    },
    {
      title: t('admin.broadcast.col.createdAt'),
      render: (row) => <span className="text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
    },
    {
      title: t('admin.broadcast.col.finishedAt'),
      render: (row) => (
        <span className="text-[13px] text-ink-3">{row.finished_at > 0 ? formatDateTime(row.finished_at) : '—'}</span>
      ),
    },
    {
      title: t('admin.broadcast.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setDetail(row)} className="text-ink-3 hover:text-brand">
            {t('admin.broadcast.action.detail')}
          </button>
          {isActiveBroadcast(row) && (
            <button type="button" onClick={() => setCancelTarget(row)} className="text-ink-3 hover:text-warn">
              {t('admin.broadcast.action.stop')}
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
            {t('admin.broadcast.title')}
            {hasActive && <Badge tone="brand">{t('admin.broadcast.activeBadge')}</Badge>}
          </h1>
          <p className="mt-0.5 text-[13px] text-ink-3">
            {t('admin.broadcast.subtitle', { total })}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={() => void load()}>
            {t('admin.broadcast.refresh')}
          </Button>
          <Button variant="primary" onClick={() => setWizardOpen(true)}>
            {t('admin.broadcast.create')}
          </Button>
        </div>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.broadcast.emptyTitle')}
          emptyDescription={t('admin.broadcast.emptyDescription')}
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
        title={t('admin.broadcast.confirm.title')}
        message={t('admin.broadcast.confirm.message', { subject: cancelTarget?.subject ?? '', pending: cancelTarget?.pending ?? 0 })}
        danger
        confirmText={t('admin.broadcast.confirm.confirmText')}
        loading={cancelLoading}
        onConfirm={handleCancel}
        onCancel={() => setCancelTarget(null)}
      />
    </div>
  )
}

/* ── 向导步骤指示条：当前步高亮，已完成打勾 ─────────────── */

function WizardSteps({ current }: { current: number }) {
  const { t } = useI18n()
  return (
    <div className="flex items-center gap-2">
      {WIZARD_STEP_KEYS.map((key, index) => {
        const step = index + 1
        const state = step < current ? 'done' : step === current ? 'active' : 'todo'
        return (
          <div key={key} className="flex items-center gap-2">
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
            <span className={`text-[13px] ${state === 'active' ? 'font-medium text-ink' : 'text-ink-3'}`}>{t(key)}</span>
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
  const { t } = useI18n()
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
      .catch((err) => toastError(err instanceof Error ? err.message : t('admin.broadcast.wizard.templateLoadFailed')))
      .finally(() => setTemplatesLoading(false))
  }, [open])

  const selectedLabel = templates.find((tpl) => tpl.key === selected)?.label ?? selected

  async function handlePreview() {
    setPreviewing(true)
    try {
      const result = await previewBroadcast(selected)
      setPreview(result)
      toast(t('admin.broadcast.wizard.previewSentToast', { to: result.to }))
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.broadcast.wizard.previewFailed'))
    } finally {
      setPreviewing(false)
    }
  }

  async function handleCreate() {
    setCreating(true)
    try {
      const bc = await createBroadcast({ template: selected, confirm: true })
      toast(t('admin.broadcast.wizard.createdToast', { total: bc.total }))
      setConfirmOpen(false)
      onCreated()
      onClose()
    } catch (err) {
      // 弹窗保持打开：后端 message 会说明失败原因（如未配置邮件通道），允许修正后重试
      toastError(err instanceof Error ? err.message : t('admin.broadcast.wizard.createFailed'))
    } finally {
      setCreating(false)
    }
  }

  /** 发送规则的静态说明（第 2 步用）：集中一处定义，避免口径漂移 */
  const rules = (
    <ul className="mt-1.5 list-disc space-y-1 pl-4 leading-relaxed">
      <li>{t('admin.broadcast.wizard.rule1')}</li>
      <li>{t('admin.broadcast.wizard.rule2')}</li>
      <li>{t('admin.broadcast.wizard.rule3')}</li>
    </ul>
  )

  return (
    <>
      <Modal open={open} onClose={onClose} title={t('admin.broadcast.wizard.title')} width={560}>
        <div className="space-y-5">
          <WizardSteps current={step} />

          {step === 1 && (
            <div className="space-y-3">
              {templatesLoading ? (
                <SkeletonRows rows={3} />
              ) : templates.length === 0 ? (
                <EmptyState
                  title={t('admin.broadcast.wizard.templatesEmptyTitle')}
                  description={t('admin.broadcast.wizard.templatesEmptyDescription')}
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
                <div className="font-medium text-ink">{t('admin.broadcast.wizard.rulesTitle')}</div>
                {rules}
              </div>
              <div className="text-sm text-ink-2">
                {t('admin.broadcast.wizard.selectedTemplate')}<span className="font-medium text-ink">{selectedLabel}</span>
              </div>
              <div>
                <Button variant="secondary" loading={previewing} onClick={handlePreview}>
                  {t('admin.broadcast.wizard.previewBtn')}
                </Button>
              </div>
              {preview && (
                <div className="rounded-lg border border-ok/25 bg-ok/5 p-3 text-[13px] text-ink-2">
                  <div className="font-medium text-ok">{t('admin.broadcast.wizard.previewSentTitle')}</div>
                  <div className="mt-1">{t('admin.broadcast.wizard.previewTo', { to: preview.to })}</div>
                  <div>{t('admin.broadcast.wizard.previewSubject', { subject: preview.subject })}</div>
                  <div className="mt-1 text-xs text-ink-3">
                    {t('admin.broadcast.wizard.previewHint')}
                  </div>
                </div>
              )}
              {!preview && (
                <div className="text-xs text-ink-3">{t('admin.broadcast.wizard.previewRequired')}</div>
              )}
            </div>
          )}

          {step === 3 && (
            <div className="space-y-4">
              <div className="rounded-lg border border-err/25 bg-err/5 p-4 text-[13px] text-ink-2">
                <div className="font-medium text-err">{t('admin.broadcast.wizard.finalTitle')}</div>
                <ul className="mt-1.5 list-disc space-y-1 pl-4 leading-relaxed">
                  <li>
                    {t('admin.broadcast.wizard.finalTemplate', { label: selectedLabel, key: selected })}
                  </li>
                  <li>{t('admin.broadcast.wizard.finalSubject', { subject: preview?.subject ?? '—' })}</li>
                  <li>{t('admin.broadcast.wizard.finalRecipients')}</li>
                  <li>{t('admin.broadcast.wizard.finalInbox', { to: preview?.to ?? '—' })}</li>
                </ul>
              </div>
              <div className="text-xs text-ink-3">{t('admin.broadcast.wizard.finalNote')}</div>
            </div>
          )}
        </div>

        <div className="mt-5 flex justify-end gap-2">
          {step > 1 && (
            <Button variant="ghost" onClick={() => setStep(step - 1)}>
              {t('admin.broadcast.wizard.prev')}
            </Button>
          )}
          {step < 3 ? (
            <Button
              variant="primary"
              disabled={step === 1 ? !selected : !preview}
              onClick={() => setStep(step + 1)}
            >
              {t('admin.broadcast.wizard.next')}
            </Button>
          ) : (
            <Button variant="danger" onClick={() => setConfirmOpen(true)}>
              {t('admin.broadcast.wizard.confirmSend')}
            </Button>
          )}
        </div>
      </Modal>

      {/* 最终闸门：与后端 confirm 字段对应的显式二次确认 */}
      <ConfirmDialog
        open={confirmOpen}
        title={t('admin.broadcast.wizard.confirmTitle')}
        message={t('admin.broadcast.wizard.confirmMessage', { subject: preview?.subject ?? selectedLabel })}
        danger
        confirmText={t('admin.broadcast.wizard.confirmText')}
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
  const { t } = useI18n()

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
      toastError(err instanceof Error ? err.message : t('admin.broadcast.recipients.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [broadcast, page, status, toastError, t])

  useEffect(() => {
    void load()
  }, [load])

  const columns: Column<BroadcastRecipient>[] = [
    { title: t('admin.broadcast.recipients.col.user'), width: 'w-16', render: (row) => <span className="text-ink-3">#{row.user_id}</span> },
    { title: t('admin.broadcast.recipients.col.email'), render: (row) => <span className="break-all text-ink">{row.email}</span> },
    { title: t('admin.broadcast.recipients.col.status'), render: (row) => <Badge tone={RECIPIENT_META[row.status].tone}>{t(RECIPIENT_META[row.status].key)}</Badge> },
    {
      title: t('admin.broadcast.recipients.col.error'),
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
      title: t('admin.broadcast.recipients.col.sentAt'),
      render: (row) => <span className="text-[13px] text-ink-3">{row.sent_at > 0 ? formatDateTime(row.sent_at) : '—'}</span>,
    },
  ]

  return (
    <Modal open onClose={onClose} title={t('admin.broadcast.recipients.title')} width={720}>
      <div className="space-y-4">
        {/* 批次概要：不依赖父级轮询数据，展示打开时的快照；发送中的批次可用「刷新」看到最新明细 */}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate text-sm font-medium text-ink">{broadcast.subject}</span>
              <Badge tone={STATUS_META[broadcast.status].tone}>{t(STATUS_META[broadcast.status].key)}</Badge>
            </div>
            <div className="mt-1 text-xs text-ink-3">
              {t('admin.broadcast.recipients.summary', {
                id: broadcast.id,
                template: broadcast.template,
                sent: broadcast.sent,
                failed: broadcast.failed,
                pending: broadcast.pending,
              })}
            </div>
          </div>
          <Button variant="secondary" size="sm" onClick={() => void load()}>
            {t('admin.broadcast.recipients.refresh')}
          </Button>
        </div>

        <Tabs
          items={[
            { value: '', label: t('admin.broadcast.recipients.tabAll') },
            { value: 'sent', label: t('admin.broadcast.recipients.tabSent') },
            { value: 'failed', label: t('admin.broadcast.recipients.tabFailed') },
            { value: 'pending', label: t('admin.broadcast.recipients.tabPending') },
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
            emptyTitle={t('admin.broadcast.recipients.emptyTitle')}
            emptyDescription={t('admin.broadcast.recipients.emptyDescription')}
          />
          <div className="pt-3">
            <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
          </div>
        </div>
      </div>
    </Modal>
  )
}
