/**
 * 管理后台页面词条（英语 / English）。
 *
 * 意图（Why）：
 *   后台 25 个页面的文案原先硬编码在 JSX 里，切到英文时整块界面仍是中文。
 *   把文案抽到词条层之后，语言切换才真正对后台生效。
 *
 * 流转（Flow）：
 *   i18n/index.ts 合并六种语言 → 页面通过 t('admin.channels.*') 读取。
 *
 * 扩展（Extend）：
 *   键集合必须与 zh-CN/admin.ts 完全一致（index.ts 用 satisfies 在编译期强制）。
 *   本文件是译文，改文案请先改中文基准再同步，避免两种语言说法漂移。
 */
export default {
  channels: {
    // ── Header ──────────────────────────────────────────────
    title: 'Channels',
    emptyTitle: 'No channels yet',
    subtitle: 'Upstream channels ({total})',
    create: 'New channel',

    // ── Table columns ───────────────────────────────────────
    col: {
      name: 'Name',
      type: 'Type',
      models: 'Models',
      keyPool: 'Key pool',
      status: 'Status',
      probe: 'Latency / probe',
      actions: 'Actions',
    },

    // ── Table cells ─────────────────────────────────────────
    cell: {
      singleKey: 'Single key',
      degradedCount: '{count} degraded',
      modelCount: '{count} models',
      allModels: 'All models',
    },

    // ── Row actions ─────────────────────────────────────────
    action: {
      speedtest: 'Speed',
      probe: 'Probe',
      cost: 'Cost',
      edit: 'Edit',
      disable: 'Disable',
      enable: 'Enable',
      delete: 'Delete',
    },

    // ── Probe result cell ────────────────────────────────────
    probe: {
      notTested: 'Not probed',
      notTestedHint: 'The background health check probes on its next cycle; you can also probe it now',
      ok: 'Healthy',
      fail: 'Failing',
      testedAt: 'Probed {time} ({relative})',
      testedAtUnknown: 'Probed at an unknown time',
      testedModel: 'Model {model}',
      networkDown: 'No network connection',
    },

    /**
     * Status code → what to do next.
     * Only covers codes whose remedy is unambiguous; guessing wrong is
     * worse than staying silent.
     */
    probeHint: {
      unreachable: 'Unreachable: check the upstream URL, egress and DNS',
      authFailed: 'Auth failed: the upstream key is invalid or expired — replace it',
      modelNotFound: 'Model not found: clean up the model names on this channel or use the upstream\'s real ones',
      rateLimited: 'Rate limited: lower the call rate or add keys to the pool',
      upstreamDown: 'Upstream outage: wait for them to recover',
    },

    // ── Probe result modal ───────────────────────────────────
    result: {
      title: 'Channel probe result',
      pass: 'Passed',
      fail: 'Failed',
      elapsed: 'Took {ms}ms',
      upstreamBody: 'Upstream response',
    },

    // ── Delete confirmation ──────────────────────────────────
    deleteDialog: {
      title: 'Delete channel',
      message: 'Delete channel "{name}"? Calls through this channel will start failing immediately.',
    },

    // ── Form (shared by create / edit) ──────────────────────
    form: {
      createTitle: 'New channel',
      editTitle: 'Edit channel',
      name: 'Channel name',
      namePlaceholder: 'Give this upstream a name',
      type: 'Channel type',
      typeFallbackPlaceholder: 'Channel type ID',
      baseUrl: 'Upstream URL',
      apiKey: 'Upstream key',
      apiKeyHelpKeep: 'Leave blank to keep the current key',
      apiKeyHelpNew: 'Also accepts a key pool: one key per line, with an optional note after a space or comma',
      keyPool: 'Key pool (optional)',
      keyPoolHelp: 'One key per line — paste several upstream keys at once',
      strategy: 'Scheduling strategy',
      failurePolicy: 'Failure handling',
      followDefault: 'Follow system default',
      models: 'Model list',
      modelsHelp: 'One per line; blank means every model is allowed',
      fetchModelsHint: 'You can fetch the list from upstream — results are merged into the field below',
      fetchFromUpstream: 'Fetch',
      fetchModels: 'Fetch models from upstream',
      useSavedCredential: 'Uses this channel\'s saved URL and key',
      useTypedCredential: 'Uses the URL and key entered above',
      fetchedCount: 'Fetched {count} models',
      fillModels: 'Insert into model list',
      copyList: 'Copy list',
      emptyFetched: 'Upstream returned an empty list',
      enable: 'Enable channel',
    },

    // ── Validation ──────────────────────────────────────────
    validation: {
      fetchNeedsUrl: 'Enter an upstream URL first',
      nameRequired: 'Enter a channel name',
      keyRequired: 'A new channel needs an upstream key',
      urlRequired: 'A new channel needs an upstream URL',
    },

    // ── Action results ──────────────────────────────────────
    toast: {
      fetched: 'Fetched {count} models — click "Insert into model list" to apply',
      fetchFailed: 'Fetch failed',
      applied: 'Inserted: {added} added, {total} in total',
      created: 'Channel created',
      updated: 'Channel updated',
      deleted: 'Channel deleted',
      deleteFailed: 'Delete failed',
      probeFailed: 'Probe failed',
      saveFailed: 'Save failed',
    },

    // ── Form footer buttons ──────────────────────────────────
    submitCreate: 'Create',
    submitSave: 'Save',
    cancel: 'Cancel',
  },
}
