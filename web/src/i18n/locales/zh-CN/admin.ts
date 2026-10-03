/**
 * 管理后台页面词条（简体中文）。
 *
 * 意图（Why）：
 *   为「页面级」文案提供词条。原先 admin.ts 是空壳，后台 25 个页面的文案
 *   全部硬编码在 JSX 里——中文环境下看不出问题，但切到其他语言时
 *   整块界面仍是中文，混合得比"全中文"更糟。
 *   把文案抽到这里之后，语言切换才真正对后台生效。
 *
 * 键名规则：
 *   admin.<页面>.<语义组>.<语义>，语义组用 camelCase。
 *   按页面分二级分组是为了让同一页的文案聚在一起便于维护，
 *   而不是平铺成几百个键——后者改文案时根本找不到对应的键。
 *
 * 插值：
 *   用 {var} 占位（t('admin.channels.cell.modelCount', { count: 3 })）。
 *   不用字符串拼接：拼接会让"整句翻译"退化成"半句半拼接"，
 *   各语言语序不同，拼接出来的句子在部分语言下会直接错乱。
 *
 * 流转（Flow）：
 *   i18n/index.ts 合并六种语言 → 页面通过 t('admin.channels.*') 读取。
 *
 * 扩展（Extend）：
 *   补词条时六种语言的同一路径都要补齐（index.ts 用 satisfies 强制键集合一致）。
 */
export default {
  channels: {
    // ── 页头 ────────────────────────────────────────────────
    title: '渠道管理',
    subtitle: '上游接入渠道（{total}）',
    create: '新建渠道',
    /** 列表为空时的标题（"还没有渠道"是口语说法，直译成英文会很生硬） */
    emptyTitle: '还没有渠道',

    // ── 表格列头 ────────────────────────────────────────────
    col: {
      name: '名称',
      type: '类型',
      models: '模型',
      keyPool: '密钥池',
      status: '状态',
      probe: '延迟 / 测活',
      actions: '操作',
    },

    // ── 表格单元格 ──────────────────────────────────────────
    cell: {
      /** 单渠道（没有配置密钥池）时的说明 */
      singleKey: '单密钥',
      /** 密钥池异常数徽标：{count} 异常 */
      degradedCount: '{count} 异常',
      /** 模型数量：{count} 个模型 */
      modelCount: '{count} 个模型',
      /** 未限定模型（模型列表为空 = 转发全部） */
      allModels: '全部模型',
    },

    // ── 行内操作 ────────────────────────────────────────────
    action: {
      speedtest: '测速',
      probe: '测活',
      cost: '进价',
      edit: '编辑',
      disable: '停用',
      enable: '启用',
      delete: '删除',
    },

    // ── 测活结论（ProbeCell）─────────────────────────────────
    probe: {
      /** 尚未测过时的徽标 */
      notTested: '未测',
      /** 未测过的悬停说明——把"为什么没测"讲清楚，否则管理员会以为是 bug */
      notTestedHint: '后台巡检会在下一个周期自动测活，也可以现在手动点「测活」',
      ok: '正常',
      fail: '异常',
      /** 测活时间：测于 {time}（{relative}） */
      testedAt: '测于 {time}（{relative}）',
      testedAtUnknown: '测于未知时间',
      /** 测活所用模型：模型 {model} */
      testedModel: '模型 {model}',
      /** 有状态码时显示 HTTP xxx；无状态码说明连状态都没拿到 */
      networkDown: '网络层未连通',
    },

    /**
     * 状态码 → 下一步该做什么。
     * 只覆盖常见且处置方式明确的几种，其余不臆测——
     * 猜错处置方向比不给提示更糟。
     */
    probeHint: {
      /** 无状态码：连接都没建立 */
      unreachable: '连不上：检查上游地址、出网与 DNS',
      /** 401/403：密钥问题 */
      authFailed: '鉴权失败：上游密钥无效或已过期，换一把',
      /** 404：模型名不对 */
      modelNotFound: '模型不存在：清理渠道里的模型名或改用上游真实模型',
      /** 429：限流 */
      rateLimited: '被限流：降低调用频率或补充密钥池',
      /** 5xx：上游自己的问题 */
      upstreamDown: '上游故障：等待对方恢复',
    },

    // ── 测活结果弹层 ────────────────────────────────────────
    result: {
      title: '渠道测活结果',
      pass: '通过',
      fail: '失败',
      /** 耗时 {ms} 毫秒 */
      elapsed: '耗时 {ms}ms',
      upstreamBody: '上游响应',
    },

    // ── 删除确认 ────────────────────────────────────────────
    deleteDialog: {
      title: '删除渠道',
      /** 确认文案：确认删除渠道「{name}」？删除后该渠道的调用将立即失败。 */
      message: '确认删除渠道「{name}」？删除后该渠道的调用将立即失败。',
    },

    // ── 表单（新建 / 编辑共用）─────────────────────────────
    form: {
      createTitle: '新建渠道',
      editTitle: '编辑渠道',
      name: '渠道名称',
      namePlaceholder: '给这个上游起个名字',
      type: '渠道类型',
      typeFallbackPlaceholder: '渠道类型编号',
      baseUrl: '上游地址',
      apiKey: '上游密钥',
      /** 编辑态：留空即不修改 */
      apiKeyHelpKeep: '留空表示不修改',
      /** 新建态：说明同时支持密钥池 */
      apiKeyHelpNew: '同时支持密钥池：每行一把，可用空格或逗号附备注',
      keyPool: '密钥池（可选）',
      keyPoolHelp: '每行一把，批量粘贴多把上游密钥',
      strategy: '调度策略',
      failurePolicy: '失败处置',
      /** 新建时的"不选 = 用后端默认"选项 */
      followDefault: '跟随系统默认',
      models: '模型列表',
      modelsHelp: '每行一个；留空表示支持全部模型',
      fetchModelsHint: '支持从上游自动拉取清单，结果合并到下方',
      fetchFromUpstream: '从上游获取',
      fetchModels: '从上游拉取模型',
      /** 拉取时用哪套凭据的说明 */
      useSavedCredential: '使用该渠道已保存的地址与密钥',
      useTypedCredential: '使用上方填写的地址与密钥',
      /** 拉取结果条：拉取到 {count} 个模型 */
      fetchedCount: '拉取到 {count} 个模型',
      fillModels: '填入模型列表',
      copyList: '复制清单',
      emptyFetched: '上游返回了空清单',
      enable: '启用渠道',
    },

    // ── 表单校验 ────────────────────────────────────────────
    validation: {
      /** 拉取模型时既没有已存凭据也没填地址 */
      fetchNeedsUrl: '请先填写上游地址',
      nameRequired: '请填写渠道名称',
      keyRequired: '新建渠道必须填写上游密钥',
      urlRequired: '新建渠道必须填写上游地址',
    },

    // ── 操作结果提示 ────────────────────────────────────────
    toast: {
      /** 拉取成功：拉取到 {count} 个模型，确认后点「填入模型列表」 */
      fetched: '拉取到 {count} 个模型，确认后点「填入模型列表」',
      fetchFailed: '拉取失败',
      /** 合并结果：已填入：新增 {added} 个，当前共 {total} 个 */
      applied: '已填入：新增 {added} 个，当前共 {total} 个',
      created: '渠道已创建',
      updated: '渠道已更新',
      deleted: '渠道已删除',
      deleteFailed: '删除失败',
      probeFailed: '测活失败',
      saveFailed: '保存失败',
    },

    // ── 表单底部按钮 ────────────────────────────────────────
    submitCreate: '创建',
    submitSave: '保存',
    cancel: '取消',
  },
}
