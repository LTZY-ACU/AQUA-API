// 本文件实现「ChatGPT 订阅账号额度查询」：把账号的套餐与额度窗口取回来。
//
// 意图（Why）：
//
//	订阅账号有两个配额维度，站长最关心的是"哪个账号快满了"：
//	  - 钱（我们自己的额度体系）——与订阅账号无关；
//	  - 上游额度窗口（5 小时 / 7 天用量百分比 + 重置时间）——只有上游知道。
//	不主动去问，就只能等账号被上限流了才从错误里发现，
//	而那时用户已经拿到一次失败。
//
//	因此这里提供一个"按需探测"能力：后台点一下就能看到每个账号的
//	套餐、已用百分比与重置时间；探测结果落库后，调度会自动跳过已用满的账号
//	（见 model.ChannelKey.QuotaExhausted），窗口一过又自动回池。
//
// 流转（Flow）：
//
//	后台「查询额度」→ server.handleProbeChannelKeyQuota
//	  └─ relay.QueryCodexQuota → 必要时先刷新 access_token → GET /wham/usage
//	       └─ 解析 → server 落库 UpdateQuota → 返回给前端展示
//
// 扩展（Extend）：
//
//	上游调整额度响应结构时：只改 codexUsageResponse 与 toCodexQuota，
//	调用方与落库逻辑不受影响。
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/channeltype"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// codexUsagePath 是 ChatGPT 的额度查询端点（相对 backend-api 根）。
//
// 注意它不在 /codex 之下：对话端点是 /backend-api/codex/responses，
// 而额度端点挂在 /backend-api/wham/usage。两者只有共同前缀
// /backend-api，因此不能拿渠道的 base_url 直接拼。
const codexUsagePath = "/wham/usage"

// codexBackendOriginFallback 是推导不出 backend-api 根时使用的兜底地址。
//
// 只有在站长把渠道地址改成了非标准值时才会用到；标准渠道一律走推导，
// 这样"自建镜像"与"官方地址"两种部署都能用。
const codexBackendOriginFallback = "https://chatgpt.com/backend-api"

// codexQuotaTimeout 是额度查询的超时。
//
// 取 20 秒：这是一次纯读的轻量请求，正常在 1 秒内返回；
// 超时快速失败好过让后台页面长时间转圈。
const codexQuotaTimeout = 20 * time.Second

// CodexQuota 是一个订阅账号的额度快照。
//
// 上游把额度拆成两个【互相独立】的窗口：5 小时窗口（primary）与每周窗口（secondary）。
// 只解析主窗口会在周额度将满时毫无预警——表现是"明明没怎么用，账号却突然全满"。
// 因此这里两个窗口都取回，并各自带上窗口时长，供界面分别渲染两条进度条。
type CodexQuota struct {
	// PlanType 是套餐标识（plus / pro / team…）；空串表示上游未提供。
	PlanType string `json:"plan_type"`
	// Email 是账号邮箱；用于界面辨认"这是谁的账号"。
	Email string `json:"email"`

	// UsedPercent 是主窗口（5 小时）已用百分比（0~100）；QuotaUsedPercentUnknown 表示未取到。
	UsedPercent int `json:"used_percent"`
	// ResetAt 是主窗口的重置时间；零值表示上游未提供。
	ResetAt time.Time `json:"reset_at"`
	// PrimaryWindowSeconds 是主窗口时长（秒）；0 表示上游未提供。
	PrimaryWindowSeconds int `json:"primary_window_seconds"`

	// SecondaryUsedPercent 是次窗口（每周）已用百分比；QuotaUsedPercentUnknown 表示未取到。
	//
	// 注意：它【不】参与调度判定（QuotaExhausted 仍只看主窗口），
	// 仅落库与展示，保证引入次窗口后既有调度行为逐字不变。
	SecondaryUsedPercent int `json:"secondary_used_percent"`
	// SecondaryResetAt 是次窗口的重置时间；零值表示上游未提供。
	SecondaryResetAt time.Time `json:"secondary_reset_at"`
	// SecondaryWindowSeconds 是次窗口时长（秒）；0 表示上游未提供。
	SecondaryWindowSeconds int `json:"secondary_window_seconds"`

	// LimitReached 表示上游明确告知"当前已触顶"。
	LimitReached bool `json:"limit_reached"`
}

// codexUsageResponse 是额度端点的响应视图（只取我们用得到的字段）。
type codexUsageResponse struct {
	Email     string `json:"email"`
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		Allowed       bool `json:"allowed"`
		LimitReached  bool `json:"limit_reached"`
		PrimaryWindow *struct {
			UsedPercent       float64 `json:"used_percent"`
			LimitWindowSecs   int     `json:"limit_window_seconds"`
			ResetAfterSeconds int     `json:"reset_after_seconds"`
			ResetAt           int64   `json:"reset_at"`
		} `json:"primary_window"`
		SecondaryWindow *struct {
			UsedPercent       float64 `json:"used_percent"`
			LimitWindowSecs   int     `json:"limit_window_seconds"`
			ResetAfterSeconds int     `json:"reset_after_seconds"`
			ResetAt           int64   `json:"reset_at"`
		} `json:"secondary_window"`
	} `json:"rate_limit"`
}

// QueryCodexQuota 查询一个订阅账号的额度快照。
//
// 参数 channel 用于推导端点地址（支持自建镜像），key 是凭据记录。
// 返回的错误已去除上游原始响应细节——它会被回显到后台界面。
func (r *Relay) QueryCodexQuota(ctx context.Context, channel *model.Channel, key *model.ChannelKey) (*CodexQuota, error) {
	if key == nil {
		return nil, fmt.Errorf("relay: 凭据为空")
	}
	if !key.IsOAuth() {
		return nil, fmt.Errorf("relay: 只有订阅账号（OAuth 凭据）才有额度窗口，API Key 无此信息")
	}
	if channel == nil {
		return nil, fmt.Errorf("relay: 渠道为空")
	}

	accessToken := strings.TrimSpace(key.AccessToken)
	if r.oauth != nil && key.NeedsRefresh(time.Now()) {
		fresh, err := r.oauth.EnsureFresh(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("relay: 刷新访问令牌失败: %w", err)
		}
		accessToken = fresh
	}
	if accessToken == "" {
		return nil, fmt.Errorf("relay: 该账号没有可用的访问令牌，请重新导入或等待自动刷新")
	}

	accountID := strings.TrimSpace(key.AccountID)
	if accountID == "" {
		// 与出站请求同样的兜底：账号标识可能只存在于令牌里。
		accountID = codexAccountIDFromToken(accessToken)
	}
	if accountID == "" {
		return nil, fmt.Errorf("relay: 该账号缺少账号标识（chatgpt_account_id），请重新导入")
	}

	endpoint := codexUsageEndpoint(channel.BaseURL)
	reqCtx, cancel := context.WithTimeout(ctx, codexQuotaTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("relay: 构造额度查询请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set(chatgptAccountIDHeader, accountID)
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		// 不回传底层错误：它可能包含内网地址或代理凭据
		return nil, fmt.Errorf("relay: 连接额度端点失败（请检查网络与账号状态）")
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := readAllLimited(resp.Body, maxUpstreamErrorBytes)
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("relay: 额度端点返回 HTTP %d：%s",
			resp.StatusCode, truncateForMessage(string(raw), 200))
	}

	var payload codexUsageResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("relay: 额度响应不是合法 JSON（HTTP %d）", resp.StatusCode)
	}
	return toCodexQuota(&payload), nil
}

// toCodexQuota 把上游响应转成额度快照（主 / 次两个窗口都填）。
//
// 缺失字段的处理：上游对"没有窗口"的账号会返回 null，
// 此时把已用百分比记为"未知"而不是 0——0 是"完全没用"，
// 两者在界面上的含义完全不同。
//
// 主窗口决定调度（触顶即记 100%），次窗口只落库展示；
// 两个窗口各自独立解析，互不借用数值（否则周用量会被误当成 5 小时用量）。
func toCodexQuota(payload *codexUsageResponse) *CodexQuota {
	quota := &CodexQuota{
		PlanType:             strings.TrimSpace(payload.PlanType),
		Email:                strings.TrimSpace(payload.Email),
		UsedPercent:          model.QuotaUsedPercentUnknown,
		SecondaryUsedPercent: model.QuotaUsedPercentUnknown,
		LimitReached:         payload.RateLimit != nil && payload.RateLimit.LimitReached,
	}
	if payload.RateLimit == nil {
		return quota
	}

	// 主窗口（5 小时）：驱动调度判定。
	if window := payload.RateLimit.PrimaryWindow; window != nil {
		quota.UsedPercent = normalizeUsedPercent(window.UsedPercent)
		quota.PrimaryWindowSeconds = normalizeWindowSeconds(window.LimitWindowSecs)
		quota.ResetAt = resolveResetAt(window.ResetAt, window.ResetAfterSeconds)
		// 上游说"已触顶"但百分比还没到 100（窗口切换瞬间可能如此）：
		// 以触顶为准，否则调度会把请求继续派给它并立刻失败。
		if quota.LimitReached && quota.UsedPercent < 100 {
			quota.UsedPercent = 100
		}
	}

	// 次窗口（每周）：仅展示与落库，不参与调度判定。
	if window := payload.RateLimit.SecondaryWindow; window != nil {
		quota.SecondaryUsedPercent = normalizeUsedPercent(window.UsedPercent)
		quota.SecondaryWindowSeconds = normalizeWindowSeconds(window.LimitWindowSecs)
		quota.SecondaryResetAt = resolveResetAt(window.ResetAt, window.ResetAfterSeconds)
	}
	return quota
}

// resolveResetAt 从上游给的"绝对时间 / 剩余秒数"里推导窗口重置时刻。
//
// 两个字段的优先级：绝对时间（reset_at）优先，缺失时才用剩余秒数换算——
// 绝对时间不受本地时钟与请求耗时影响，比"now + 剩余秒"更准。
// 都没有时返回零值（表示未知）。
func resolveResetAt(resetAt int64, resetAfterSeconds int) time.Time {
	switch {
	case resetAt > 0:
		return time.Unix(resetAt, 0)
	case resetAfterSeconds > 0:
		return time.Now().Add(time.Duration(resetAfterSeconds) * time.Second)
	default:
		return time.Time{}
	}
}

// normalizeWindowSeconds 把上游给的窗口时长收敛为非负数。
//
// 负数没有意义（上游实现差异），归零表示"未提供"；
// 界面上据此退回到默认文案（5 小时 / 每周），而不是显示一个负时长。
func normalizeWindowSeconds(seconds int) int {
	if seconds < 0 {
		return 0
	}
	return seconds
}

// normalizeUsedPercent 把上游的百分比收敛到 0~100 的整数。
//
// 上游可能返回 100.5 或负数（实现差异），越界值会让调度判断失准：
// 超过 100 会被当成"未满"而继续派发，负数会被当成"未探测"。
func normalizeUsedPercent(value float64) int {
	if value < 0 {
		return model.QuotaUsedPercentUnknown
	}
	if value > 100 {
		return 100
	}
	return int(value + 0.5)
}

// codexUsageEndpoint 由渠道地址推导额度端点。
//
// 推导规则：渠道地址形如 `https://host/backend-api/codex`，
// 而额度端点在 `https://host/backend-api/wham/usage`——两者共享 `/backend-api` 前缀。
// 因此按该前缀切分再拼上额度路径；切不出来（站长填了非标准地址）时用官方兜底。
func codexUsageEndpoint(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")

	if idx := strings.Index(trimmed, "/backend-api"); idx > 0 {
		return trimmed[:idx] + "/backend-api" + codexUsagePath
	}
	if trimmed != "" {
		// 既不是标准形态，也不像自建镜像：按"把额度端点挂在同源根下"猜测，
		// 猜错时上游会返回 404，错误信息里能看到实际请求的地址，便于定位。
		if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return parsed.Scheme + "://" + parsed.Host + "/backend-api" + codexUsagePath
		}
	}
	return codexBackendOriginFallback + codexUsagePath
}

// truncateForMessage 截断要放进错误信息的文本。
//
// 上游错误原话对排查有价值，但整段 HTML/JSON 塞进错误提示会淹没界面。
func truncateForMessage(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "…"
}

// codexQuotaResetHint 从上游 429 响应里解析"何时恢复"的提示。
//
// 订阅账号被限流时，上游通常会明确告知恢复时间（额度窗口重置或限流解除）。
// 用它做冷却时长比指数退避准确得多：退避给的是几十秒，而账号实际要等两小时——
// 那期间每次轮询到它都会白试一次，还会把失败次数推高。
//
// 返回值 <= 0 表示没有可用提示，调用方应回退到默认退避。
func codexQuotaResetHint(body []byte) time.Duration {
	if len(body) == 0 {
		return 0
	}

	var envelope struct {
		Error *struct {
			Type            string `json:"type"`
			ResetsAt        int64  `json:"resets_at"`
			ResetsInSeconds int64  `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error == nil {
		return 0
	}

	// 只有"配额类"错误才采信：普通限流（短时突发）用退避反而更合适。
	switch strings.ToLower(strings.TrimSpace(envelope.Error.Type)) {
	case "usage_limit_reached", "rate_limit_exceeded", "gousagelimiterror":
	default:
		return 0
	}

	switch {
	case envelope.Error.ResetsAt > 0:
		return clampCodexResetHint(time.Until(time.Unix(envelope.Error.ResetsAt, 0)))
	case envelope.Error.ResetsInSeconds > 0:
		return clampCodexResetHint(time.Duration(envelope.Error.ResetsInSeconds) * time.Second)
	default:
		return 0
	}
}

// codexQuotaResetMin / Max 是采信上游恢复提示的区间。
//
// 太短（< 1 分钟）：退避本身就够了，用上游提示没有收益；
// 太长（> 24 小时）：多半是上游给了异常值，照做等于把账号长期闲置，
// 宁可回退到退避，让它在下一轮请求里再试一次。
const (
	codexQuotaResetMin = time.Minute
	codexQuotaResetMax = 24 * time.Hour
)

// clampCodexResetHint 把提示时长夹到可信区间；超出区间返回 0（表示不采信）。
func clampCodexResetHint(hint time.Duration) time.Duration {
	if hint < codexQuotaResetMin || hint > codexQuotaResetMax {
		return 0
	}
	return hint
}

// codexChannelSpec 判断渠道是否使用 Codex 协议（供调用方做前置校验）。
func codexChannelSpec(channel *model.Channel) (channeltype.Type, bool) {
	if channel == nil {
		return channeltype.Type{}, false
	}
	spec, ok := channeltype.Find(strings.TrimSpace(channel.TypeKey))
	if !ok || spec.Protocol != channeltype.ProtocolCodex {
		return channeltype.Type{}, false
	}
	return spec, true
}
