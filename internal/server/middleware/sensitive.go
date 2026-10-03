// 本文件实现「敏感词过滤」中间件：在请求进入上游之前扫描正文，命中即拒绝。
//
// 意图（Why）：
//
//	站长作为生成能力的提供方，需要对明显违规的输入有最低成本的管控手段；
//	同时部分上游会因内容违规封禁整个渠道，损失落在站长身上。
//	把过滤做在【网关入口】有三个好处：
//	  1) 命中的请求根本不转发上游，不产生上游成本、也不计费；
//	  2) 不依赖任何第三方内容审核服务，零额外成本、零外部依赖；
//	  3) 词表完全由站长掌握，可以随时增删而不必等待上游支持。
//
// 为什么放在 /v1 分组的中间件里、而不是各转发处理器内部：
//
//	转发处理器（relay）刻意只依赖 net/http，把 HTTP 前置逻辑塞进去会破坏这层解耦；
//	而"读一次请求体、扫描、原样还原"对所有 POST 端点都是同一件事，集中实现只有一份。
//
// 读取请求体的安全性（关键）：
//
//	与审计中间件同一套做法——读完后必须把"已读部分 + 剩余流"拼回去，
//	否则下游处理器会读到空请求体（表现为"一开过滤全部请求都报 JSON 格式错误"）。
//
// 失败策略（重要取舍）：
//
//	本中间件【读不到配置或词表时放行】（fail-open）。
//	理由：过滤是可用性之上的附加能力；若因一次读库抖动就把全站请求都拒掉，
//	造成的损失远大于"几秒钟没过滤"。真正需要强一致的场景应在上游侧做审核。
//
// 流转（Flow）：
//
//	router 在 /v1 与 /v1beta 分组挂载 SensitiveFilter.Middleware()
//	  └─ snapshot：按 TTL 读取"总开关 + 编译好的词表匹配器"
//	       ├─ 未开启 → 放行（不读请求体，零开销）
//	       └─ 已开启 → 仅对 POST 读体（有上限）
//	            ├─ 提取 JSON 中的文本值（跳过 base64 等二进制串）
//	            ├─ Aho–Corasick 扫描
//	            └─ 命中 → 400 content_filter（并记 Warn 日志，含命中分类，不含词本身）
//
// 扩展（Extend）：
//
//	新增"仅记录不拦截"或"输出侧过滤"时：本文件保持入口扫描职责不变，
//	另建中间件/处理器并复用 model.SensitiveMatcher 即可。
package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
)

const (
	// sensitiveCacheTTL 是"配置 + 词表"的缓存存活时间。
	//
	// 与计费价格缓存同理：太短等于每次请求都查库，太长会让站长改完词表却看不到效果。
	// 30 秒 + 后台改词后主动 Invalidate，实际感知是即时的。
	sensitiveCacheTTL = 30 * time.Second

	// sensitiveBodyLimit 是参与扫描的请求体上限（1 MiB）。
	//
	// 超过这个体积的请求体几乎都是携带 Base64 图片的多模态请求，
	// 其中不含自然语言正文；强行全量扫描只会浪费 CPU 并可能对 base64
	// 文本产生误判。超限时跳过扫描并记一条 Debug 日志。
	sensitiveBodyLimit = 1 << 20

	// sensitiveScanRunes 是单次请求参与匹配的最大字符数（按 rune 计）。
	//
	// 匹配本身是 O(n)，这里再设一个上限是为了给"超长上下文拼接"设防线：
	// 20 万字符已远超正常对话正文，超过部分不再扫描。
	sensitiveScanRunes = 200_000

	// sensitiveBinaryHintRunes 是"可能是二进制串"的判断阈值。
	//
	// 长的、不含任何空白与常见标点的字符串只可能是 base64 / 哈希 / 密钥，
	// 不可能是自然语言；对它们做关键词匹配会产生荒唐的误判
	//（例如 base64 里恰好出现某几个英文字母的组合）。
	sensitiveBinaryHintRunes = 1024
)

// SensitiveFilter 是敏感词过滤组件，并发安全。
//
// 词表变化时【整体替换】编译好的匹配器，而不是就地修改：
// 匹配路径因此完全无锁，也不会出现"半个新词表"的中间态。
type SensitiveFilter struct {
	words    model.SensitiveWordRepository
	settings model.SettingRepository

	mu    sync.RWMutex
	state *sensitiveState
}

// sensitiveState 是"当前生效的过滤状态"快照。
type sensitiveState struct {
	enabled  bool
	matcher  *model.SensitiveMatcher
	loadedAt time.Time
}

// NewSensitiveFilter 创建过滤组件。
//
// words 或 settings 为 nil 时中间件退化为"直接放行"（便于测试与"不需要过滤"的部署）。
func NewSensitiveFilter(words model.SensitiveWordRepository, settings model.SettingRepository) *SensitiveFilter {
	return &SensitiveFilter{words: words, settings: settings}
}

// Invalidate 丢弃当前快照，使下一次请求重新读取配置与词表。
//
// 调用时机：后台增删改敏感词或切换总开关之后。
// 不做这件事的后果：站长改完词表要等最多 30 秒才生效，会被当成"改了没用"。
func (f *SensitiveFilter) Invalidate() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.state = nil
	f.mu.Unlock()
}

// Middleware 返回过滤中间件。
func (f *SensitiveFilter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if f == nil || f.words == nil || f.settings == nil {
			c.Next()
			return
		}
		// 只扫描"提交内容"的 POST：GET /v1/models、GET /v1/tasks/{ref} 没有正文，
		// 扫它们只是白白读一次请求体并增加延迟。
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		state := f.snapshot(c.Request.Context())
		if state == nil || !state.enabled || state.matcher.Empty() {
			c.Next()
			return
		}

		hit, matched := f.scan(c.Request)
		if !matched {
			c.Next()
			return
		}

		// 日志里只记分类、不记词本身，也不记完整正文：
		// 日志会被站长之外的人看到（运维、排障），回显词表等于把黑名单公开，
		// 使用者只要试探几次就能摸清规则并绕过。
		slog.Warn("请求内容命中敏感词，已拒绝",
			"category", hit.Category,
			"client_ip", ClientIP(c),
			"path", c.Request.URL.Path)

		abortWithError(c, http.StatusBadRequest,
			"请求内容包含不被允许的表述，请调整后重试", oai.TypeContentFilter, oai.CodeSensitiveWordBlocked)
	}
}

// snapshot 返回当前生效的过滤状态；缓存过期时重新加载。
func (f *SensitiveFilter) snapshot(ctx context.Context) *sensitiveState {
	f.mu.RLock()
	cached := f.state
	fresh := cached != nil && time.Since(cached.loadedAt) < sensitiveCacheTTL
	f.mu.RUnlock()

	if fresh {
		return cached
	}
	return f.reload(ctx, cached)
}

// reload 重新加载"总开关 + 词表"，并写回快照。
//
// 容错：读取失败时沿用旧快照（宁可短时间沿用旧词表，也不要因为一次读库失败
// 就把过滤整体关掉——那正是黑名单最需要生效的时刻之一）。
// 无旧快照可用时返回"未启用"，即放行。
func (f *SensitiveFilter) reload(ctx context.Context, old *sensitiveState) *sensitiveState {
	settings, err := model.LoadSiteSettings(ctx, f.settings)
	if err != nil {
		slog.Warn("读取敏感词过滤开关失败，本次沿用旧状态", "error", err)
		if old != nil {
			// 沿用旧状态但刷新时间戳，避免每次请求都重试读库。
			return &sensitiveState{enabled: old.enabled, matcher: old.matcher, loadedAt: time.Now()}
		}
		return &sensitiveState{enabled: false, matcher: model.NewSensitiveMatcher(nil), loadedAt: time.Now()}
	}

	next := &sensitiveState{enabled: settings.Safeguard.SensitiveFilterEnabled, loadedAt: time.Now()}
	if !next.enabled {
		// 未开启：不加载词表（省一次查询），并清空匹配器。
		next.matcher = model.NewSensitiveMatcher(nil)
		f.store(next)
		return next
	}

	words, err := f.words.List(ctx, true)
	if err != nil {
		slog.Warn("读取敏感词表失败，本次沿用旧词表", "error", err)
		if old != nil && old.matcher != nil {
			next.matcher = old.matcher
		} else {
			next.matcher = model.NewSensitiveMatcher(nil)
		}
		f.store(next)
		return next
	}

	next.matcher = model.NewSensitiveMatcher(words)
	f.store(next)
	return next
}

// store 写回快照。
func (f *SensitiveFilter) store(state *sensitiveState) {
	f.mu.Lock()
	f.state = state
	f.mu.Unlock()
}

// scan 读取请求体并扫描其中的文本；返回命中的词条。
//
// 无论是否命中，请求体都会被完整还原，保证下游处理器行为不受影响。
func (f *SensitiveFilter) scan(req *http.Request) (*model.SensitiveWord, bool) {
	if req.Body == nil {
		return nil, false
	}

	// 多读 1 字节用于判断是否超限（LimitReader 会在上限处静默截断）。
	prefix, err := io.ReadAll(io.LimitReader(req.Body, sensitiveBodyLimit+1))
	// 先还原完整请求体，再做后续判断：任何分支下都不能让下游读到空体。
	req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(prefix), req.Body))
	if err != nil {
		slog.Debug("读取请求体用于敏感词扫描失败，本次跳过扫描", "error", err)
		return nil, false
	}
	if len(prefix) > sensitiveBodyLimit {
		slog.Debug("请求体超过敏感词扫描上限，跳过扫描", "limit", sensitiveBodyLimit)
		return nil, false
	}

	text := extractSensitiveText(prefix, sensitiveScanRunes)
	if text == "" {
		return nil, false
	}

	f.mu.RLock()
	state := f.state
	f.mu.RUnlock()
	if state == nil || state.matcher == nil {
		return nil, false
	}
	return state.matcher.Match(text)
}

// extractSensitiveText 从请求体中提取"需要参与匹配的文本"。
//
// JSON 请求体会被解析后只取【字符串值】（跳过键名）：
// 键名是协议字段（model / role / type…），把键名也算进去只会增加误判；
// 非 JSON 请求体则整体当作文本处理。
func extractSensitiveText(raw []byte, maxRunes int) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}

	var parsed any
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return truncateRunes(string(trimmed), maxRunes)
	}

	collector := &textCollector{limit: maxRunes}
	collector.append(parsed)
	return collector.String()
}

// textCollector 收集参与匹配的文本，并按【字符数】设上限。
//
// 单独成类型而不是直接用 strings.Builder：Builder.Len() 返回的是字节数，
// 而我们的上限是按字符（rune）定义的；中文下一个字符三字节，
// 混用两种单位会让上限在中英文场景下的实际含义相差三倍。
type textCollector struct {
	builder strings.Builder
	runes   int
	limit   int
}

func (c *textCollector) full() bool {
	return c.runes >= c.limit
}

func (c *textCollector) add(value string) {
	if isBinaryLikeString(value) {
		return
	}
	c.builder.WriteString(value)
	c.builder.WriteByte('\n')
	c.runes += utf8.RuneCountInString(value) + 1
}

func (c *textCollector) String() string {
	return c.builder.String()
}

// append 递归收集 JSON 中的字符串值（不收集键名）。
func (c *textCollector) append(value any) {
	if c.full() {
		return
	}

	switch typed := value.(type) {
	case string:
		c.add(typed)
	case []any:
		for _, item := range typed {
			c.append(item)
		}
	case map[string]any:
		for _, item := range typed {
			c.append(item)
		}
	}
}

// isBinaryLikeString 判断字符串是否"不可能是自然语言"（base64 / 哈希 / 密钥）。
//
// 判据：data: 前缀，或"很长且不含任何空白与常见标点"。
// 这个启发式的意义在于避免对多模态请求里的 Base64 图片做关键词匹配——
// 那会对完全正常的图片产生随机的误拦。
func isBinaryLikeString(value string) bool {
	if strings.HasPrefix(value, "data:") {
		return true
	}
	if utf8.RuneCountInString(value) < sensitiveBinaryHintRunes {
		return false
	}
	return !strings.ContainsAny(value, " \t\n\r，。！？、；：,.!?;:")
}
