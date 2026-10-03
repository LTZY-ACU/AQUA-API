/** 管理后台：订阅账号 OAuth 提供方（/admin/oauth）。列表 + 新建/编辑弹层（client_secret 留空不修改）+ 删除；数据经 api/admin.ts 读写 /api/admin/oauth-providers。 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { createOAuthProvider, deleteOAuthProvider, listOAuthProviders, updateOAuthProvider } from '@/api/admin'
import type { OAuthProvider, OAuthProviderPayload } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input, Switch } from '@/components/ui/Form'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'

export default function AdminOAuthPage() {
  const [items, setItems] = useState<OAuthProvider[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<OAuthProvider | null | 'new'>(null)
  const [deleteTarget, setDeleteTarget] = useState<OAuthProvider | null>(null)
  const { toast, toastError } = useToast()
  const { t } = useI18n()

  // 接口不分页：一次取回全部提供方（数量级很小），因此本页不渲染 Pagination
  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listOAuthProviders()
      setItems(data.items)
      setTotal(data.total)
    } catch {
      /* 401 统一处理 */
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteOAuthProvider(deleteTarget.id)
      toast(t('admin.oauth.toast.deleted'))
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.oauth.toast.deleteFailed'))
    }
  }

  const columns: Column<OAuthProvider>[] = [
    { title: t('admin.oauth.col.name'), render: (row) => <span className="font-medium text-ink">{row.name}</span> },
    {
      title: t('admin.oauth.col.tokenUrl'),
      render: (row) => (
        <span className="max-w-56 block truncate text-[13px] text-ink-2" title={row.token_url}>
          {row.token_url}
        </span>
      ),
    },
    { title: t('admin.oauth.col.clientId'), render: (row) => <code className="font-mono text-[13px] text-ink-2">{row.client_id}</code> },
    { title: t('admin.oauth.col.scope'), render: (row) => <span className="text-[13px] text-ink-2">{row.scope || '—'}</span> },
    {
      title: t('admin.oauth.col.enabled'),
      render: (row) => <Badge tone={row.enabled ? 'ok' : 'off'}>{row.enabled ? t('admin.oauth.statusEnabled') : t('admin.oauth.statusDisabled')}</Badge>,
    },
    {
      title: t('admin.oauth.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setEditing(row)} className="text-ink-3 hover:text-brand">
            {t('admin.oauth.action.edit')}
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">
            {t('admin.oauth.action.delete')}
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.oauth.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.oauth.subtitle', { total })}</p>
        </div>
        <Button variant="primary" onClick={() => setEditing('new')}>
          {t('admin.oauth.create')}
        </Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.oauth.emptyTitle')}
          emptyDescription={t('admin.oauth.emptyDescription')}
        />
      </Card>

      <OAuthFormModal
        open={editing !== null}
        provider={editing === 'new' ? null : editing}
        onClose={() => setEditing(null)}
        onSaved={() => {
          setEditing(null)
          void load()
        }}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('admin.oauth.delete.title')}
        message={t('admin.oauth.delete.message', { name: deleteTarget?.name ?? '' })}
        danger
        confirmText={t('admin.oauth.delete.confirm')}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/* ── OAuth 提供方表单弹层 ──────────────────────────────── */

function OAuthFormModal({
  open,
  provider,
  onClose,
  onSaved,
}: {
  open: boolean
  provider: OAuthProvider | null
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const { t } = useI18n()
  const [name, setName] = useState('')
  const [tokenUrl, setTokenUrl] = useState('')
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [scope, setScope] = useState('')
  const [remark, setRemark] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setName(provider?.name ?? '')
    setTokenUrl(provider?.token_url ?? '')
    setClientId(provider?.client_id ?? '')
    setClientSecret('') // 明文永不回显，编辑时留空表示不修改
    setScope(provider?.scope ?? '')
    setRemark(provider?.remark ?? '')
    setEnabled(provider?.enabled ?? true)
  }, [open, provider])

  async function handleSubmit() {
    if (!name.trim()) {
      toastError(t('admin.oauth.error.nameRequired'))
      return
    }
    if (!tokenUrl.trim()) {
      toastError(t('admin.oauth.error.tokenUrlRequired'))
      return
    }
    if (!clientId.trim()) {
      toastError(t('admin.oauth.error.clientIdRequired'))
      return
    }
    if (!provider && !clientSecret.trim()) {
      toastError(t('admin.oauth.error.clientSecretRequired'))
      return
    }
    setLoading(true)
    try {
      const payload: OAuthProviderPayload = {
        name: name.trim(),
        token_url: tokenUrl.trim(),
        client_id: clientId.trim(),
        scope: scope.trim() || undefined,
        remark: remark.trim() || undefined,
        enabled,
      }
      if (provider) {
        if (clientSecret.trim()) payload.client_secret = clientSecret.trim()
        await updateOAuthProvider(provider.id, payload)
        toast(t('admin.oauth.toast.updated'))
      } else {
        payload.client_secret = clientSecret.trim()
        await createOAuthProvider(payload)
        toast(t('admin.oauth.toast.created'))
      }
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.oauth.toast.saveFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={provider ? t('admin.oauth.form.editTitle') : t('admin.oauth.form.newTitle')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.oauth.form.name')} required>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('admin.oauth.form.namePlaceholder')} />
        </Field>

        <Field label={t('admin.oauth.form.tokenUrl')} required help={t('admin.oauth.form.tokenUrlHelp')}>
          <Input value={tokenUrl} onChange={(e) => setTokenUrl(e.target.value)} placeholder="https://auth.openai.com/oauth/token" />
        </Field>

        <Field label={t('admin.oauth.form.clientId')} required>
          <Input value={clientId} onChange={(e) => setClientId(e.target.value)} placeholder={t('admin.oauth.form.clientIdPlaceholder')} />
        </Field>

        <Field label={t('admin.oauth.form.clientSecret')} required={!provider} help={provider ? t('admin.oauth.form.clientSecretHelpEdit') : t('admin.oauth.form.clientSecretHelpNew')}>
          <Input
            value={clientSecret}
            onChange={(e) => setClientSecret(e.target.value)}
            type="password"
            placeholder={provider ? t('admin.oauth.form.clientSecretPlaceholderKeep') : t('admin.oauth.form.clientSecretPlaceholderNew')}
            autoComplete="new-password"
          />
        </Field>

        <Field label={t('admin.oauth.form.scope')} help={t('admin.oauth.form.scopeHelp')}>
          <Input value={scope} onChange={(e) => setScope(e.target.value)} placeholder={t('admin.oauth.form.scopePlaceholder')} />
        </Field>

        <Field label={t('admin.oauth.form.remark')}>
          <Input value={remark} onChange={(e) => setRemark(e.target.value)} placeholder={t('admin.oauth.form.remarkPlaceholder')} />
        </Field>

        <Field label={t('admin.oauth.form.enabled')}>
          <div className="flex items-center justify-between rounded-md border border-line bg-surface px-3 py-2">
            <span className="text-[13px] text-ink-2">{t('admin.oauth.form.enabledSwitch')}</span>
            <Switch checked={enabled} onChange={setEnabled} />
          </div>
        </Field>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('admin.oauth.form.cancel')}
        </Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>
          {provider ? t('admin.oauth.form.save') : t('admin.oauth.form.create')}
        </Button>
      </div>
    </Modal>
  )
}