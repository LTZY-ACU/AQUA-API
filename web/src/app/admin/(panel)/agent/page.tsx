/** 管理后台：AI 助手（/admin/agent）。
 *
 * 意图（Why）：
 *   站长原话是"如果你不懂可以直接叫他帮你去干"。要让这句话落地，
 *   助手必须在一个地方能被【配好】、【拿到权限】、【真的用起来】——
 *   而这三件事在真实使用中是交替发生的：先试一句发现没模型、回去填模型、
 *   再试发现不能查数据、发一把密钥、才真正开始用。
 *   因此本页不是"设置表单 + 聊天窗口"两张皮，而是一页三段：
 *
 *     ①助手（对话）—— 就在最上面，因为它是最常用的
 *     ②运行配置   —— 总开关、模型、两段提示词
 *     ③密钥管理   —— 给外部人发"只能问客服"的钥匙
 *
 *   顺序刻意如此：配置写错时站长最需要的不是文档而是"问它一句"，
 *   让助手自己回答"我该配什么"比读文档更快。
 *
 * 三处安全取舍：
 *   1) 密钥明文【只显示一次】，本窗口关掉就再也拿不到（服务端只存摘要）；
 *   2) 写配置与发密钥都要过一次 reauth（useReauthGuard）——
 *      能改客服提示词 = 能改本站对外说话的嘴；能发密钥 = 能让外部人用站长的钱；
 *   3) 提示词留空 = 用内置底稿，这一点由后端下发标记告知，
 *      不在前端自己判断（理由见 api/agent.ts 的 AgentSettings 注释）。
 *
 * 流转（Flow）：
 *   本页 → api/agent.ts → /api/admin/agent/{settings,keys} 与 /api/admin/agent/chat
 *
 * 扩展（Extend）：
 *   服务端加工具时本页无需改动——工具清单随 SSE 的 tool 事件下发，
 *   界面按名字渲染，不在前端硬编码。
 */
'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'

import {
  createAgentKey,
  deleteAgentKey,
  fetchAgentKeys,
  fetchAgentSettings,
  saveAgentSettings,
  updateAgentKey,
  type AgentKeyItem,
  type AgentRole,
  type AgentSettings,
} from '@/api/agent'
import { chatWithOps } from '@/api/agent'
import { AgentChatPanel, type AskFn } from '@/components/agent/AgentChatPanel'
import { useReauthGuard } from '@/components/auth/ReauthGuard'
import { Badge, Card, Tabs } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Field, Input, Switch, Textarea } from '@/components/ui/Form'
import { ConfirmDialog, Modal } from '@/components/ui/Modal'
import { DataTable, type Column } from '@/components/ui/Table'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

type TabKey = 'chat' | 'settings' | 'keys'

export default function AdminAgentPage() {
  const { toast, toastError } = useToast()
  const { guard, dialog: reauthDialog } = useReauthGuard()

  const [tab, setTab] = useState<TabKey>('chat')
  const [settings, setSettings] = useState<AgentSettings | null>(null)
  const [keys, setKeys] = useState<AgentKeyItem[]>([])
  const [loading, setLoading] = useState(true)
  const [creating, setCreating] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<AgentKeyItem | null>(null)
  const [busyId, setBusyId] = useState(0)
  /**
   * 刚生成的密钥明文。
   *
   * 刻意独立成一个 state 而不是塞进 settings 或 keys 列表：
   * 明文的存在期只有"生成后的这一瞬间"，把它混进列表数据后，
   * "刷新页面后它还在不在"就再也一眼答不出来了——而那正是最需要随时确定的事。
   * null = 当前没有待展示的明文。
   */
  const [plainKey, setPlainKey] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [cfg, list] = await Promise.all([fetchAgentSettings(), fetchAgentKeys()])
      setSettings(cfg)
      setKeys(list)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '助手配置加载失败')
    } finally {
      setLoading(false)
    }
  }, [toastError])

  useEffect(() => {
    void load()
  }, [load])

  /**
   * 是否至少配了一个模型。
   *
   * 后端在「开启但一个模型都没填」时会拒绝（这是对的：
   * 开了却调不通的入口比关着更糟）。但只有后端拦的话，
   * 站长看到的是一句 HTTP 400 的技术文案——那正是本页最初的 bug：
   * 停用状态下点「启用助手」，必然失败，而界面看上去完全正常。
   * 因此前端先判一次，并在按钮上给出可执行的下一步，而不是让请求去撞后端。
   */
  const hasModel = useMemo(
    () =>
      Boolean(
        settings?.default_model?.trim() ||
          settings?.ops_model?.trim() ||
          settings?.support_model?.trim(),
      ),
    [settings],
  )

  /**
   * 切换总开关（顶部横幅与配置页共用）。
   *
   * 只负责请求与二次验证，【不弹成功提示】——两个调用点的提示文案不同，
   * 由各自决定。让本函数也弹一次，配置页就会连续看到两条一样的 toast。
   * 失败时仍然由本函数抛提示：那部分文案两个入口是一样的。
   */
  async function persistEnabled(next: boolean) {
    // 只在"开启"这一侧拦：停用永远该被允许（它不花钱，且是出事时的第一反应）。
    if (next && settings && !hasModel) {
      setTab('settings')
      toastError('请先在「运行配置」里填至少一个模型，再启用助手')
      return
    }
    try {
      // 总开关是【花钱的开关】：开着就意味着任何拿到客服密钥的人
      // 都能消耗站长的上游额度。因此要过 reauth。
      const saved = await guard(() => saveAgentSettings({ enabled: next }))
      setSettings(saved)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '启停失败')
      throw err
    }
  }

  /** 顶部横幅的「启用助手」按钮：成功提示由这里给。 */
  async function handleToggleEnabled(next: boolean) {
    try {
      await persistEnabled(next)
      // persistEnabled 在"缺模型"时静默返回（已就地提示并跳到配置页），
      // 这里靠返回值区分：没真正切换就不该再报成功。
      if (next && !hasModel) return
      toast(next ? '助手已启用' : '助手已停用，两个入口同时关闭')
    } catch {
      // 失败提示已由 persistEnabled 给出，这里不再重复。
    }
  }

  async function handleToggleKeyStatus(row: AgentKeyItem) {
    setBusyId(row.id)
    try {
      await guard(() => updateAgentKey(row.id, { status: row.status === 1 ? 2 : 1 }))
      toast(row.status === 1 ? '密钥已停用' : '密钥已启用')
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '密钥状态更新失败')
    } finally {
      setBusyId(0)
    }
  }

  async function handleDeleteKey() {
    if (!deleteTarget) return
    try {
      await guard(() => deleteAgentKey(deleteTarget.id))
      toast('密钥已删除，该密钥立即失效')
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '删除失败')
    }
  }

  // 把 api 层的 chatWithOps 适配成本组件的 AskFn。
  // 这一层薄适配放在页面里而不是组件里：组件不该知道三种凭据的区别。
  const ask: AskFn = useCallback(
    async (input, handlers, signal) => {
      await chatWithOps(input, (event) => {
        switch (event.type) {
          case 'delta':
            handlers.onDelta(event.text)
            break
          case 'tool':
            handlers.onTool(event.tool.name, event.tool.mutating)
            break
          case 'done':
            handlers.onDone(event.result)
            break
          case 'error':
            handlers.onError(event.message)
            break
          case 'end':
            break
        }
      }, signal)
    },
    [],
  )

  const activeKeyCount = useMemo(() => keys.filter((k) => k.active).length, [keys])

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">AI 助手</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">
            你可以直接在这里问它站点的事（能查数据、能改配置），
            也可以把「在线客服」开放给用户登录后使用
          </p>
        </div>
        <div className="flex items-center gap-3">
          <StatusPill settings={settings} />
          <Button variant="secondary" onClick={() => void load()} loading={loading}>
            刷新
          </Button>
        </div>
      </div>

      {!settings?.enabled && (
        <Card>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div className="min-w-0">
              <div className="text-[13px] font-semibold text-ink">助手当前是停用状态</div>
              {hasModel ? (
                <p className="mt-1 text-[13px] leading-relaxed text-ink-3">
                  打开后可以在这里直接问它；对外开放前建议先在本站试几轮，
                  确认它答得靠谱——客服的话术由你决定，但答错的代价也是你承担。
                </p>
              ) : (
                // 没配模型时不给"启用"按钮：给一个必然失败的按钮，
                // 是在让站长自己撞一次 400 才能发现少了一步。
                <p className="mt-1 text-[13px] leading-relaxed text-ink-3">
                  启用前需要先指定一个模型（助手靠它回答，每次对话都会产生上游费用）。
                </p>
              )}
            </div>
            {hasModel ? (
              <Button variant="primary" onClick={() => void handleToggleEnabled(true)}>
                启用助手
              </Button>
            ) : (
              <Button variant="primary" onClick={() => setTab('settings')}>
                去填模型
              </Button>
            )}
          </div>
        </Card>
      )}

      <Tabs<TabKey>
        value={tab}
        onChange={setTab}
        items={[
          { value: 'chat', label: '助手对话' },
          { value: 'settings', label: '运行配置' },
          { value: 'keys', label: '密钥管理', count: keys.length },
        ]}
      />

      {tab === 'chat' && (
        <Card>
          {settings?.enabled ? (
            <AgentChatPanel
              ask={ask}
              showTools
              emptyTitle="问我站点里的事"
              emptyDescription="我能查渠道、令牌、订单、用户、日志，也能改部分配置。比如「现在有哪些渠道熔断了」「帮我看看哪个用户欠费」或「把 X 渠道权重调成 5」。不确定问什么时，先问「你能做什么」。"
              footerHint="助手能改动线上配置。涉及删除、封禁这类不可逆操作时我会先跟你确认。"
            />
          ) : (
            <DisabledHint />
          )}
        </Card>
      )}

      {tab === 'settings' && settings && (
        <SettingsForm
          settings={settings}
          onSaved={(next) => {
            setSettings(next)
            void load()
          }}
        />
      )}

      {tab === 'keys' && (
        <>
          <Card padding="none">
            <DataTable
              columns={keyColumns({
                busyId,
                onToggleStatus: handleToggleKeyStatus,
                onDelete: setDeleteTarget,
              })}
              rows={loading ? null : keys}
              loading={loading}
              rowKey={(row) => row.id}
              emptyTitle="还没有发放任何密钥"
              emptyDescription="密钥用于让网站外部的人（小程序、第三方站点、脚本）直接问在线客服，不需要登录本站账号。若只想让本站登录用户使用，可以不发密钥——用户门户里已经有客服入口。"
            />
          </Card>
          <Card>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-[13px] text-ink-2">
                当前 {keys.length} 把密钥，其中 {activeKeyCount} 把可用
              </div>
              <Button variant="primary" onClick={() => setCreating(true)}>
                发放客服密钥
              </Button>
            </div>
          </Card>
        </>
      )}

      <CreateKeyModal
        open={creating}
        onClose={() => setCreating(false)}
        onSubmit={async (payload) => {
          const created = await guard(() => createAgentKey(payload))
          setCreating(false)
          void load()
          setPlainKey(created.key)
        }}
      />

      <PlainKeyModal plainKey={plainKey} onClose={() => setPlainKey(null)} />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title="删除客服密钥"
        message={`确认删除「${deleteTarget?.name || ''}」？删除后正在使用它的一方会立刻收到 401，且无法恢复。`}
        danger
        confirmText="删除"
        onConfirm={handleDeleteKey}
        onCancel={() => setDeleteTarget(null)}
      />

      {reauthDialog}
    </div>
  )
}

/* ── 状态与空态 ─────────────────────────────────────────────── */

/**
 * StatusPill：总开关的当前状态。
 *
 * 做成"看得见但不能点"的一颗药丸而不是开关本身：
 * 停用助手要过密码验证，不该由一次误点触发。
 */
function StatusPill({ settings }: { settings: AgentSettings | null }) {
  if (!settings) return null
  if (!settings.enabled) return <Badge tone="off">已停用</Badge>
  const model = settings.ops_model || settings.default_model
  return (
    <Badge tone="ok">
      运行中{model ? ` · ${model}` : ''}
    </Badge>
  )
}

function DisabledHint() {
  return (
    <div className="flex flex-col items-center justify-center px-4 py-14 text-center">
      <div className="text-[15px] font-semibold text-ink">助手尚未启用</div>
      <p className="mt-1.5 max-w-md text-[13px] leading-relaxed text-ink-3">
        启用前请先到「运行配置」指定模型并确认两段提示词。总开关关闭时，
        后台对话与所有客服入口都会返回「助手未启用」——这是有意的：
        助手每次对话都会消耗上游额度。
      </p>
    </div>
  )
}

/* ── 运行配置 ───────────────────────────────────────────────── */

function SettingsForm({
  settings,
  onSaved,
}: {
  settings: AgentSettings
  onSaved: (next: AgentSettings) => void
}) {
  const { toast, toastError } = useToast()
  const [defaultModel, setDefaultModel] = useState(settings.default_model)
  const [opsModel, setOpsModel] = useState(settings.ops_model)
  const [supportModel, setSupportModel] = useState(settings.support_model)
  const [opsPrompt, setOpsPrompt] = useState(settings.ops_system_prompt)
  const [supportPrompt, setSupportPrompt] = useState(settings.support_system_prompt)
  const [historyEnabled, setHistoryEnabled] = useState(settings.history_enabled)
  const [saving, setSaving] = useState(false)

  /**
   * 表单里三个模型输入框是否至少有一个非空。
   *
   * 注意判断的是【输入框里的当前值】而不是 settings 里的旧值：
   * 用户可能刚把模型名清掉，此时保存就该被拦下，
   * 而不能让请求带着"空模型 + 已开启"去撞后端的 400。
   */
  const hasAnyModel =
    Boolean(defaultModel.trim() || opsModel.trim() || supportModel.trim())

  async function handleSave() {
    setSaving(true)
    try {
      const saved = await saveAgentSettings({
        default_model: defaultModel.trim(),
        ops_model: opsModel.trim(),
        support_model: supportModel.trim(),
        ops_system_prompt: opsPrompt,
        support_system_prompt: supportPrompt,
        history_enabled: historyEnabled,
      })
      onSaved(saved)
      toast('配置已保存')
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  /**
   * 总开关在配置页的落点：把本表单连同开关一起落盘。
   *
   * 两个刻意的设计：
   *
   * 1) 先落盘表单，再切开关——而不是先发一个只带 enabled 的请求。
   *    只带 enabled 的话，"清空模型名 → 点开关"这种很自然的操作必然 400，
   *    而用户完全不知道自己漏了哪一步。把表单一起提交，开关与内容
   *    天然一致，也不会出现"界面显示已开、实际模型还是上一份"的错位状态。
   *
   * 2) 不复用顶部横幅的 persistEnabled：那个函数带二次验证且失败时已报过错，
   *    在这里再包一层 catch 只会让同一次失败弹两条一样的提示。
   *    配置页自己发请求，由本函数独占错误处理。
   */
  async function handleToggleFromForm(next: boolean) {
    // 开启时先校验本地表单：空着就别去让后端拒绝，那只会得到一句技术文案。
    if (next && !hasAnyModel) {
      toastError('请至少填写一个模型（默认模型、运维模型、客服模型任选其一）')
      return
    }
    setSaving(true)
    try {
      // 注意 enabled 一并带上：这不是"保存表单 + 另切开关"两个请求，
      // 而是一次提交，避免中间态被别人读到。
      const saved = await saveAgentSettings({
        enabled: next,
        default_model: defaultModel.trim(),
        ops_model: opsModel.trim(),
        support_model: supportModel.trim(),
        ops_system_prompt: opsPrompt,
        support_system_prompt: supportPrompt,
        history_enabled: historyEnabled,
      })
      onSaved(saved)
      toast(next ? '助手已启用' : '助手已停用，两个入口同时关闭')
    } catch (err) {
      toastError(err instanceof Error ? err.message : '启停失败')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-5">
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div className="text-[13px] font-semibold text-ink">总开关</div>
            <p className="mt-0.5 text-[13px] text-ink-3">
              关闭后后台助手与所有客服入口一并停用（对外接口返回「助手未启用」）
            </p>
          </div>
          <Switch
            checked={settings.enabled}
            onChange={(next) => void handleToggleFromForm(next)}
            label="启用 AI 助手"
          />
        </div>
      </Card>

      <Card>
        <h2 className="text-[13px] font-semibold text-ink">模型选择</h2>
        <p className="mt-0.5 mb-4 text-[13px] text-ink-3">
          留空则使用默认模型。客服不能自行切换模型（否则人人能让你付贵模型的钱），
          只有运维助手可以在对话时临时指定。
        </p>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="默认模型" help="两个角色都没单独指定时用它">
            <Input
              value={defaultModel}
              onChange={(e) => setDefaultModel(e.target.value)}
              placeholder="例如 gpt-4o-mini"
              autoComplete="off"
            />
          </Field>
          <Field label="运维助手模型" help="建议用推理更强的">
            <Input
              value={opsModel}
              onChange={(e) => setOpsModel(e.target.value)}
              placeholder="留空 = 用默认"
              autoComplete="off"
            />
          </Field>
          <Field label="在线客服模型" help="建议用便宜快的">
            <Input
              value={supportModel}
              onChange={(e) => setSupportModel(e.target.value)}
              placeholder="留空 = 用默认"
              autoComplete="off"
            />
          </Field>
        </div>

        {/* 三项全空时立刻说清后果，而不是等用户点开关再被后端 400 打回。
            这条提示与上面的 help 措辞不同：help 讲"留空会怎样"，
            这里讲"你现在的状态会导致什么"——前者是规则，后者是当前诊断。 */}
        {!hasAnyModel && (
          <p className="mt-3 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-[12px] leading-relaxed text-warn">
            三个模型都还是空的。至少填一个才能启用助手——它靠模型回答，
            没有模型时所有对话请求都会失败。
          </p>
        )}
      </Card>

      <Card>
        <h2 className="text-[13px] font-semibold text-ink">提示词</h2>
        <p className="mt-0.5 mb-4 text-[13px] text-ink-3">
          留空表示使用内置底稿。你改提示词只能改变它的说话方式，
          【改变不了它的权限边界】——在线客服无论被怎么提示都拿不到站内数据。
        </p>
        <div className="space-y-4">
          <Field
            label="运维助手提示词"
            help={
              settings.ops_prompt_is_default
                ? '当前留空将使用内置底稿'
                : '已自定义（留空可恢复为内置底稿）'
            }
          >
            <Textarea
              value={opsPrompt}
              onChange={(e) => setOpsPrompt(e.target.value)}
              placeholder={settings.prompt_placeholder}
              rows={7}
            />
          </Field>
          <Field
            label="在线客服提示词"
            help={
              settings.support_prompt_is_default
                ? '当前留空将使用内置底稿'
                : '已自定义（留空可恢复为内置底稿）'
            }
          >
            <Textarea
              value={supportPrompt}
              onChange={(e) => setSupportPrompt(e.target.value)}
              placeholder={settings.prompt_placeholder}
              rows={7}
            />
          </Field>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>
              携带多轮上下文
              <span className="ml-2 text-[12px] text-ink-3">
                关掉可缩短响应并省钱，但客服会"忘记"上一句说了什么
              </span>
            </span>
            <Switch
              checked={historyEnabled}
              onChange={setHistoryEnabled}
              label="携带多轮上下文"
            />
          </label>
        </div>

        <div className="mt-5 flex justify-end">
          <Button variant="primary" loading={saving} onClick={() => void handleSave()}>
            保存配置
          </Button>
        </div>
      </Card>
    </div>
  )
}

/* ── 密钥表格 ───────────────────────────────────────────────── */

function keyColumns({
  busyId,
  onToggleStatus,
  onDelete,
}: {
  busyId: number
  onToggleStatus: (row: AgentKeyItem) => void
  onDelete: (row: AgentKeyItem) => void
}): Column<AgentKeyItem>[] {
  return [
    {
      title: '备注',
      render: (row) => (
        <div className="min-w-0">
          <div className="truncate text-[13px] font-medium text-ink" title={row.name}>
            {row.name}
          </div>
          <div className="mt-0.5 text-[12px] text-ink-3">{row.role_label}</div>
        </div>
      ),
    },
    {
      title: '状态',
      align: 'center',
      render: (row) => {
        if (row.active) return <Badge tone="ok">可用</Badge>
        if (row.expired) return <Badge tone="warn">已过期</Badge>
        return <Badge tone="off">已停用</Badge>
      },
    },
    {
      title: '有效期',
      className: 'hidden lg:table-cell',
      render: (row) => (
        <span className="whitespace-nowrap text-[13px] text-ink-3">
          {row.expires_at ? formatDateTime(row.expires_at) : '永久'}
        </span>
      ),
    },
    {
      title: '创建时间',
      className: 'hidden xl:table-cell',
      render: (row) => (
        <span className="whitespace-nowrap text-[13px] text-ink-3">
          {formatDateTime(row.created_at)}
        </span>
      ),
    },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-3 text-[13px]">
          <button
            type="button"
            disabled={busyId === row.id}
            onClick={() => onToggleStatus(row)}
            className="text-brand hover:underline disabled:opacity-50"
          >
            {row.status === 1 ? '停用' : '启用'}
          </button>
          <button
            type="button"
            disabled={busyId === row.id}
            onClick={() => onDelete(row)}
            className="text-ink-3 hover:text-err disabled:opacity-50"
          >
            删除
          </button>
        </span>
      ),
    },
  ]
}

/* ── 发放密钥 ───────────────────────────────────────────────── */

function CreateKeyModal({
  open,
  onClose,
  onSubmit,
}: {
  open: boolean
  onClose: () => void
  onSubmit: (payload: { role: AgentRole; name: string; expires_in_days: number }) => Promise<void>
}) {
  const { toastError } = useToast()
  const [name, setName] = useState('')
  const [expires, setExpires] = useState(0)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setName('')
    setExpires(0)
  }, [open])

  async function handleSubmit() {
    if (!name.trim()) {
      toastError('请填写备注，方便日后认出这把密钥发给谁了')
      return
    }
    setLoading(true)
    try {
      await onSubmit({ role: 'support', name: name.trim(), expires_in_days: expires })
    } catch (err) {
      toastError(err instanceof Error ? err.message : '发放失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="发放客服密钥" width={560}>
      <div className="space-y-4">
        <Field label="用途备注" required help="只你自己可见。写清楚发给谁了，密钥泄露时能立刻定位范围。">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：某某论坛的客服入口"
            autoComplete="off"
          />
        </Field>
        <Field label="有效期" help="默认永久有效。对方是外部站点时建议设期限，泄露后影响面可控。">
          <div className="flex gap-2">
            {[0, 7, 30, 90].map((d) => (
              <button
                key={d}
                type="button"
                onClick={() => setExpires(d)}
                className={`rounded-md border px-3 py-1.5 text-[13px] transition ${
                  expires === d
                    ? 'border-brand bg-brand/10 text-brand'
                    : 'border-line-2 text-ink-2 hover:border-ink-3'
                }`}
              >
                {d === 0 ? '永久' : `${d} 天`}
              </button>
            ))}
          </div>
        </Field>
        <p className="rounded-md bg-surface px-3 py-2.5 text-[12px] leading-relaxed text-ink-3">
          客服密钥只能提问、不能碰站内数据，也无法自行切换模型。
          若只是想让本站登录用户使用客服，不必发密钥——用户门户里已有入口。
        </p>
      </div>

      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          取消
        </Button>
        <Button variant="primary" loading={loading} onClick={() => void handleSubmit()}>
          生成密钥
        </Button>
      </div>
    </Modal>
  )
}

/* ── 一次性明文展示 ─────────────────────────────────────────── */

function PlainKeyModal({ plainKey, onClose }: { plainKey: string | null; onClose: () => void }) {
  const { toast } = useToast()

  async function handleCopy() {
    if (!plainKey) return
    try {
      await navigator.clipboard.writeText(plainKey)
      toast('已复制，请立即交给需要它的人')
    } catch {
      toastErrorFallback()
    }
  }

  function toastErrorFallback() {
    // 剪贴板不可用（http 非安全上下文、浏览器拒绝）时不要静默失败：
    // 用户会以为复制好了，然后把关掉弹层当成"已经记下来了"。
    window.alert('复制失败，请手动选中下方文本复制。')
  }

  return (
    <Modal
      open={plainKey !== null}
      onClose={onClose}
      title="密钥已生成"
      width={620}
    >
      <p className="text-[13px] leading-relaxed text-ink-2">
        这把密钥<strong className="text-err">只显示这一次</strong>。
        服务端只保存它的摘要，关闭本窗口后无法再次查看。
      </p>
      <div className="mt-3 flex items-center gap-2 rounded-md border border-line-2 bg-surface px-3 py-2.5">
        <code className="min-w-0 flex-1 break-all text-[13px] text-ink">{plainKey}</code>
        <Button variant="secondary" onClick={() => void handleCopy()} className="shrink-0">
          复制
        </Button>
      </div>
      <p className="mt-3 text-[12px] leading-relaxed text-ink-3">
        调用方式：
        <code className="mx-1 rounded bg-surface px-1 py-0.5">POST /api/agent/chat</code>
        ，请求头 <code className="rounded bg-surface px-1 py-0.5">Authorization: Bearer {'{密钥}'}</code>，
        请求体 <code className="rounded bg-surface px-1 py-0.5">{'{"question":"..."}'}</code>，
        响应为 SSE 流。
      </p>

      <div className="mt-5 flex justify-end">
        <Button variant="primary" onClick={onClose}>
          我已保存
        </Button>
      </div>
    </Modal>
  )
}
