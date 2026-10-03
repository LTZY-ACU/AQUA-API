/** 弹窗与对话框：Modal（通用弹层）与 ConfirmDialog（确认框）。
 *
 * 意图（Why）：
 *   后台大量「新建/编辑/删除确认」交互集中于此。统一遮罩、滚动锁定、
 *   无关键操作丢失；焦点管理保持基本可访问性（Esc 关闭、返回值确认）。
 */
'use client'

import { Fragment, useEffect, type ReactNode } from 'react'

import { AppIcon } from '@/components/AppIcon'
import { Button } from './Button'

interface ModalProps {
  open: boolean
  onClose: () => void
  title: string
  /** 弹层宽度（默认 560px，宽表单用 720px，窄提示用 420px） */
  width?: number
  children: ReactNode
  footer?: ReactNode
}

export function Modal({ open, onClose, title, width = 560, children, footer }: ModalProps) {
  // Esc 关闭 + 打开时锁定背景滚动
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', onKey)
      document.body.style.overflow = prevOverflow
    }
  }, [open, onClose])

  if (!open) return null

  return (
    <div className="fixed inset-0 z-40 flex items-start justify-center overflow-y-auto p-4 pt-[8vh]">
      {/* 遮罩：点击关闭 */}
      <div className="fixed inset-0 bg-ink/40 backdrop-blur-[2px]" onClick={onClose} aria-hidden="true" />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        style={{ width }}
        className="relative flex max-w-full flex-col rounded-lg border border-line bg-card shadow-pop"
      >
        <div className="flex items-center justify-between border-b border-line px-5 py-3.5">
          <h3 className="text-[15px] font-semibold text-ink">{title}</h3>
          <button type="button" onClick={onClose} className="rounded p-1 text-ink-3 hover:bg-ink/5 hover:text-ink" aria-label="关闭">
            <AppIcon name="close" size={18} />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto px-5 py-4">{children}</div>
        {footer && <div className="flex justify-end gap-2 border-t border-line px-5 py-3.5">{footer}</div>}
      </div>
    </div>
  )
}

interface ConfirmDialogProps {
  open: boolean
  title: string
  message?: string
  confirmText?: string
  cancelText?: string
  danger?: boolean
  loading?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export function ConfirmDialog({
  open,
  title,
  message,
  confirmText = '确认',
  cancelText = '取消',
  danger,
  loading,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  return (
    <Modal open={open} onClose={onCancel} title={title} width={420}>
      <div className="text-sm text-ink-2">{message || '确认执行该操作？'}</div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onCancel}>
          {cancelText}
        </Button>
        <Button variant={danger ? 'danger' : 'primary'} loading={loading} onClick={onConfirm}>
          {confirmText}
        </Button>
      </div>
    </Modal>
  )
}

/* ── CopyButton：一键复制（通用） ───────────────────────── */

import { useState } from 'react'
import { copyText } from '@/utils/clipboard'
import { useToast } from '@/lib/toast/toast-context'

interface CopyButtonProps {
  text: string
  label?: string
  className?: string
}

export function CopyButton({ text, label = '复制', className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false)
  const { toastError } = useToast()
  async function handleCopy() {
    const ok = await copyText(text)
    if (ok) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } else {
      // 失败必须有明确反馈：否则用户看到按钮纹丝不动，会以为"无法复制"，
      // 而真实的根因（剪贴板被占用/权限被拒）完全被静默吞掉。
      toastError('复制失败：请手动选择并复制，或检查浏览器剪贴板权限')
    }
  }
  return (
    <button
      type="button"
      onClick={handleCopy}
      className={`inline-flex items-center gap-1 text-[13px] text-ink-3 transition hover:text-brand ${className ?? ''}`}
    >
      <AppIcon name={copied ? 'check' : 'copy'} size={14} />
      {copied ? '已复制' : label}
    </button>
  )
}