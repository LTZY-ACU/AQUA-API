/** 用户门户：第三方账号（/console/accounts）。
 *
 * 意图（Why）：
 *   「已绑定的第三方身份」与「再绑一个」两件事收在一页。
 *   绑定关系决定了"用第三方账号登录时进的是哪个本站账号"，
 *   用户必须能自己看见、自己核验——绑错了就是登录进一个空号。
 *
 * 刻意不做解绑：第三方创建的账号密码是随机生成且用户不知道的，
 * 解绑 + 退出会让人永久进不去自己的账号；真要解绑必须走站长人工处理。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { bindQIUAccount, listMyExternalAccounts, type ExternalAccount } from '@/api/auth'
import { useQIUAuthorize } from '@/components/auth/QIOLoginButton'
import { Button } from '@/components/ui/Button'
import { Badge, Card } from '@/components/ui/Display'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

export default function ConsoleAccountsPage() {
  const [items, setItems] = useState<ExternalAccount[]>([])
  const [loading, setLoading] = useState(true)
  const { toast, toastError } = useToast()
  const { waiting, authorize } = useQIUAuthorize()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listMyExternalAccounts()
      setItems(data.items ?? [])
    } catch {
      /* 401 统一处理 */
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function handleBind() {
    // 复用登录那一套等待逻辑：确认动作发生在对方页面，本站只能等。
    const authorized = await authorize()
    if (!authorized) return
    try {
      const result = await bindQIUAccount(authorized.taskId)
      toast(result.username ? `已绑定「${result.username}」` : '已绑定第三方账号')
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '绑定失败')
    }
  }

  // 同一个第三方身份只能绑一个本站账号，绑过就不再重复提供入口。
  const boundQiu = items.some((item) => item.provider === 'qiu')

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold text-ink">第三方账号</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">绑定后可以直接用对方的账号登录本站</p>
      </div>

      <Card padding="none">
        <div className="border-b border-line px-4 py-3 text-sm font-semibold text-ink-2">已绑定</div>
        {loading ? (
          <div className="px-4 py-6 text-[13px] text-ink-3">加载中…</div>
        ) : items.length === 0 ? (
          <div className="px-4 py-6 text-[13px] text-ink-3">
            还没有绑定任何第三方账号，绑定后登录时就不用记本站的密码了。
          </div>
        ) : (
          <ul className="divide-y divide-line">
            {items.map((item) => (
              <li key={`${item.provider}:${item.account_name}`} className="flex items-center justify-between gap-3 px-4 py-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-ink">{item.provider_text || item.provider}</span>
                    {!item.login_enabled && <Badge tone="warn">本站已停用该登录方式</Badge>}
                  </div>
                  <div className="mt-0.5 truncate text-[13px] text-ink-3">
                    {item.nickname ? `${item.nickname} · ` : ''}
                    {item.account_name}
                  </div>
                </div>
                <div className="shrink-0 text-right text-[13px] text-ink-3">
                  {item.bound_at ? formatDateTime(item.bound_at) : '—'}
                  <div className="text-xs">绑定时间</div>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card>
        <div className="text-sm font-semibold text-ink-2">绑定 QIU 科技账号</div>
        <p className="mt-2 text-[13px] leading-6 text-ink-3">
          点击后会打开 QIU 的确认页，在那一页点「确认登录」即可完成绑定。绑定只影响登录方式，
          不会把你当前的额度、令牌或数据搬到别处。
        </p>
        <div className="mt-4">
          <Button variant="primary" loading={waiting} disabled={boundQiu} onClick={handleBind}>
            {boundQiu ? '已绑定 QIU 科技账号' : '绑定 QIU 科技账号'}
          </Button>
        </div>
        <p className="mt-3 text-xs leading-5 text-ink-3">
          暂不支持自行解绑：由第三方登录创建的账号其密码是随机生成的，解绑又退出会让人再也进不来。
          确需解绑请联系站长人工处理。
        </p>
      </Card>
    </div>
  )
}
