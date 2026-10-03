// Package relay 是网关的核心域：把客户端的模型请求转发到选定的上游渠道。
//
// 意图（Why）：
//
//	网关存在的根本价值在于"统一入口 + 智能转发"。本包负责其中最关键的一环：
//	  1) 根据请求中的模型名，从渠道池中选出可用渠道；
//	  2) 改写目标地址与鉴权信息，向上游发起请求；
//	  3) 把上游响应（含 SSE 流式分片）原样回写给客户端。
//	M1 阶段只做"单渠道透传"——不转换协议、不做计费、不做重试；
//	这些能力会在 M2+ 按里程碑逐步加入，但接口形态保持一致。
//
// 流转（Flow）：
//
//	server/router.go
//	  └─ POST /v1/chat/completions → Relay.ServeChatCompletions
//	       ├─ 读取并解析请求体（仅取 model 字段用于路由，其余原样透传）
//	       ├─ 解析本次请求分组          groupFromContext（令牌分组 → 默认分组）
//	       ├─ Relay.SelectChannel(reqCtx, group, model)  按分组选渠道
//	       ├─ forwardChat(...)                    构造上游请求并发送
//	       └─ flushCopy(...)                      流式回写响应体
//
// 扩展（Extend）：
//
//	新增协议（如 Anthropic Messages）：新建同级的 anthropic.go，
//	  复用本文件的 SelectChannel，并在 server/router.go 注册对应路径。
//	新增路由策略（负载均衡、粘性会话）：只改 SelectChannel 的实现，
//	  调用方（openai.go 等）无需变动——这是把选渠道抽成方法的意义。
//	新增重试/熔断：在 forwardChat 外层包一层循环，配合"排除已失败渠道"。
package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/corpus"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/netguard"
	"github.com/LTZY-ACU/ltzy-api/internal/reqctx"
)

// ErrNoAvailableChannel 表示当前没有任何可用渠道能处理请求的模型。
//
// 这是运维最常见的故障信号（渠道被禁用/模型未声明），因此单独定义，
// 便于上层层返回明确的 503 与可操作的提示。
var ErrNoAvailableChannel = errors.New("relay: 没有可用的上游渠道")

// AutoModelName 是保留的模型名：请求 model 填它时，网关自动选择
// 「当前分组里延迟最低的可对话模型」来转发（见 resolveAutoChatModel）。
//
// 为什么用保留名而不是配置参数：auto 是"给调用方少记一个模型名"的体验优化，
// 固定字符串最直观，也最容易在文档与调试里说清楚。
const AutoModelName = "auto"

// UpstreamTimeout 是网关等待上游响应的时间上限（首字节）。
//
// 取值 300 秒的理由（重要，不要随意调小）：
//
//  1. 部分平台（典型如 NVIDIA NIM）本身的排队与首字节延迟就非常高，
//     几十秒才开始返回是常态；把上限设成 60 秒会把"慢但完全可用"的模型
//     判成不可用，反而自己制造故障。
//  2. 推理类模型（长思考、大输出）首字节超过一分钟很常见。
//  3. 判定"上游真的挂了"应该依据连接错误、明确的 4xx/5xx、以及超时后的换渠道重试，
//     而不是依据一个偏小的超时值——后者会把"慢"错误地等价成"坏"。
//
// 该常量同时被模型列表拉取、渠道测活与异步任务复用，目的是让全站只有
// 「一个上游超时数字」，避免出现"转发等 300 秒、但测活 15 秒就报失败"这种
// 自相矛盾的表现。
const UpstreamTimeout = 300 * time.Second

// 上游 HTTP 客户端的默认参数。
const (
	defaultGroup               = "default" // 默认分组
	defaultResponseHeaderWait  = UpstreamTimeout
	defaultTLSHandshakeTimeout = 10 * time.Second // TLS 握手超时
	defaultIdleConnTimeout     = 90 * time.Second // 空闲连接回收时间
	defaultMaxIdleConnsPerHost = 20               // 每个上游主机的空闲连接上限
)

// Options 是 Relay 的可选参数。
type Options struct {
	// Group 指定默认路由分组。为空时使用 defaultGroup。
	Group string
	// ResponseHeaderTimeout 是等待上游响应头的超时（即"首字节时间"）。
	// 注意它与"整个请求超时"不同：流式响应可能持续数分钟，
	// 我们只限制"上游多久没开始响应"，而不是"响应多久必须结束"。
	ResponseHeaderTimeout time.Duration
	// UsageLogs 为调用日志仓储；为 nil 时不记录用量（便于单元测试）。
	UsageLogs model.UsageLogRepository
	// Tokens 为访问令牌仓储，用于更新令牌的最近使用时间；可为 nil。
	Tokens model.TokenRepository
	// Keys 为渠道密钥池仓储；为 nil 时退化为"每渠道单密钥"模式（便于单元测试）。
	Keys model.ChannelKeyRepository
	// OAuth 为订阅账号令牌刷新器；为 nil 时 OAuth 凭据不会被自动刷新。
	OAuth *OAuthRefresher
	// Billing 为计费组件；为 nil 时只记录用量而不扣减额度。
	Billing *Billing
	// ChannelModelMappings 为渠道级模型映射仓储；为 nil 时禁用模型名映射改写
	// （请求与响应均按原样透传，行为与引入映射前完全一致）。
	ChannelModelMappings model.ChannelModelMappingRepository
	// Corpus 是「语料共建计划」的内存判定组件；为 nil 时完全不采集原文。
	//
	// 它只回答"这个模型要不要采"，与用户无关——采集面向全站，
	// 免计费那一半（按用户）在鉴权与计费处判定。
	Corpus *corpus.Guard
	// CorpusSamples 是语料样本仓储；与 Corpus 同时为 nil 时才彻底关闭采集。
	CorpusSamples model.CorpusRepository
	// ModelSpeeds 是模型测速结果仓储；为 nil 时禁用 auto 路由
	// （请求 model 填 auto 会按普通模型名处理 → 大概率 503），
	// 其余转发路径完全不受影响。
	ModelSpeeds model.ModelSpeedRepository
}

// Relay 是转发引擎，持有渠道仓储与上游 HTTP 客户端。
//
// 并发安全：所有字段在构造后不再修改，可被多 goroutine 共享。
type Relay struct {
	channels model.ChannelRepository
	client   *http.Client
	group    string

	// usageLogs / tokens 用于转发后记录用量与令牌使用时间，两者均可为 nil。
	usageLogs model.UsageLogRepository
	tokens    model.TokenRepository
	// keys 为渠道密钥池仓储（可选）。为 nil 时行为退化为单密钥。
	keys model.ChannelKeyRepository
	// billing 为计费组件（可选）。为 nil 时不扣费，仅记录用量。
	billing *Billing
	// oauth 为订阅账号令牌刷新器（可选）。为 nil 时 OAuth 凭据不刷新。
	oauth *OAuthRefresher
	// channelMappings 为渠道级模型映射仓储（可选）。为 nil 时禁用模型名映射。
	channelMappings model.ChannelModelMappingRepository
	// mappingCache 缓存"渠道 → 映射列表"（见 model_map.go）。仅在启用映射仓储时非 nil。
	mappingCache *channelMappingCache
	// corpus 是语料采集的判定组件（可选）。判定为"否"时链路上零开销。
	corpus *corpus.Guard
	// corpusSamples 是语料样本仓储（可选）。为 nil 时不落样本。
	corpusSamples model.CorpusRepository
	// speeds 为模型测速结果仓储（可选）。为 nil 时 auto 路由不可用。
	speeds model.ModelSpeedRepository
}

// New 创建转发引擎。
//
// 关于 HTTP 客户端的超时设计（重要）：
//
//	刻意【不设置】http.Client.Timeout。该字段覆盖"从发起请求到读完响应体"的全过程，
//	而大模型流式响应可能持续数分钟，设置后会导致长回答被强制中断。
//	正确的做法是分层控制超时：
//	  - ResponseHeaderTimeout：限制上游多久必须开始响应（防挂死）；
//	  - 客户端断连：由 request context 自动取消（http.NewRequestWithContext）；
//	  - 总时长上限：后续里程碑在业务层按模型配置。
func New(channels model.ChannelRepository, opts Options) *Relay {
	group := opts.Group
	if group == "" {
		group = defaultGroup
	}
	headerTimeout := opts.ResponseHeaderTimeout
	if headerTimeout <= 0 {
		headerTimeout = defaultResponseHeaderWait
	}

	// 仅在启用映射仓储时才建缓存：仓储为 nil 意味着该部署不使用映射，
	// 此时留空缓存可让 modelMappingsForChannel 直接短路，避免无谓开销。
	var mappingCache *channelMappingCache
	if opts.ChannelModelMappings != nil {
		mappingCache = newChannelMappingCache(defaultMappingCacheTTL, defaultMappingCacheMax)
	}

	// 默认分组一致性对齐（防回归）。
	//
	// 路由用本函数的 group 选渠道，而计费组件用它自己的默认分组查价格；
	// 两者若不同，不带分组的令牌就会"按 A 组选渠道、按 B 组查价格"——
	// 查不到价格的模型会被判为【不计费】，于是收费模型被免费调用
	// （余额 0 也能调），而账单上看不出任何异常。
	//
	// 这里直接【对齐】而不是只告警：这是启动期的一次性装配，对齐后系统状态自洽，
	// 从根上消除这一类故障；同时打错误日志，配置问题依然可见。
	if opts.Billing != nil && opts.Billing.DefaultGroup() != group {
		slog.Error("计费默认分组与路由默认分组不一致，已按路由分组对齐",
			"relay_group", group, "billing_group", opts.Billing.DefaultGroup())
		opts.Billing.SetDefaultGroup(group)
	}

	return &Relay{
		channels:        channels,
		group:           group,
		usageLogs:       opts.UsageLogs,
		tokens:          opts.Tokens,
		keys:            opts.Keys,
		billing:         opts.Billing,
		oauth:           opts.OAuth,
		channelMappings: opts.ChannelModelMappings,
		mappingCache:    mappingCache,
		corpus:          opts.Corpus,
		corpusSamples:   opts.CorpusSamples,
		speeds:          opts.ModelSpeeds,
		client: &http.Client{
			Transport: &http.Transport{
				// 走系统代理环境变量：便于在受限网络中经代理访问上游
				Proxy: http.ProxyFromEnvironment,

				// 建连前的地址护栏：拒绝链路本地与云元数据地址（见 netguard 包）。
				// 放在拨号层而不是只校验 URL：域名可能在"校验通过"之后才解析到内网地址。
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
					Control:   netguard.DialControl,
				}).DialContext,

				// 连接复用：网关是高频转发场景，复用连接可显著降低延迟
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   defaultMaxIdleConnsPerHost,
				IdleConnTimeout:       defaultIdleConnTimeout,
				TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
				ExpectContinueTimeout: 1 * time.Second,

				// 上游"必须开始响应"的时限，避免连接挂死占用资源
				ResponseHeaderTimeout: headerTimeout,

				// 上游普遍支持 HTTP/2，开启可提升多路复用效率
				ForceAttemptHTTP2: true,
			},
		},
	}
}

// listCandidates 查询指定分组的启用渠道，并过滤出支持该模型的候选。
//
// 返回结果保持仓储给出的顺序：优先级降序 → 权重降序 → ID 升序。
// 路由逻辑依赖"优先级降序"这一前提来分层，故不可在此重排。
//
// 为什么"一次查询、多次挑选"：故障转移会在同一请求内多次选渠道。
// 若每次重试都查一次库，故障场景下会把数据库压力放大数倍；
// 而渠道配置在单次请求的毫秒级窗口内几乎不会变化，缓存于内存是安全且划算的。
//
// 参数 group 为空时回退到 Relay 的默认分组：这样"未指定分组的调用方"
// （如内部调用或尚未接入分组的老代码）行为与改动前完全一致。
func (r *Relay) listCandidates(ctx context.Context, group, modelName string) ([]*model.Channel, error) {
	if group == "" {
		group = r.group
	}
	enabled := model.ChannelStatusEnabled

	channels, err := r.channels.List(ctx, model.ChannelQuery{
		Group:  group,
		Status: &enabled,
	})
	if err != nil {
		return nil, fmt.Errorf("relay: 查询候选渠道失败: %w", err)
	}

	eligible := make([]*model.Channel, 0, len(channels))
	for _, ch := range channels {
		// 过渡约定：模型列表为空 = 支持全部模型（便于只配一个渠道做连通性验证）。
		// 该行为将在引入严格模式后收紧。
		if len(ch.Models) == 0 || ch.HasModel(modelName) {
			eligible = append(eligible, ch)
		}
	}
	return eligible, nil
}

// channelCountForModel 统计"任意启用渠道声明支持该模型"的数量（不限定分组）。
//
// 用途：转发层在"当前分组没有可用渠道"时区分两种情形——模型在别的分组可用
// （可见性配置问题，应提示使用者检查分组）还是全站都没有渠道（容量问题）。
// 判定口径与 listCandidates 的模型匹配一致（空模型清单 = 支持全部模型）。
func (r *Relay) channelCountForModel(ctx context.Context, modelName string) int {
	if r.channels == nil {
		return 0
	}
	enabled := model.ChannelStatusEnabled
	channels, err := r.channels.List(ctx, model.ChannelQuery{Status: &enabled, Limit: 500})
	if err != nil {
		return 0 // 查询失败按"无渠道"处理：宁可回笼统文案，也不阻塞请求
	}
	count := 0
	for _, ch := range channels {
		if len(ch.Models) == 0 || ch.HasModel(modelName) {
			count++
		}
	}
	return count
}

// groupFromContext 解析「本次请求应使用的分组」。
//
// 解析优先级（顺序不可变，改错会造成"用 A 分组选渠道、用 B 分组计费"的错账）：
//  1. 请求上下文里的令牌分组（非空）→ 用它；
//  2. 令牌分组为空 → Relay 的默认分组；
//  3. 上下文里没有令牌（内部调用，如异步任务/后台流程）→ Relay 的默认分组。
//
// 之所以只在此处解析一次、并把结果沿转发链路往下传：渠道选择、失败重试换渠道、
// 计费必须使用同一个分组值；任何一处重新解析都可能因取值来源不同而串组。
func (r *Relay) groupFromContext(ctx context.Context) string {
	if group := strings.TrimSpace(reqctx.Group(ctx)); group != "" {
		return group
	}
	return r.group
}

// resolveAutoChatModel 解析 auto 模型名 → 「延迟最低的可对话模型」。
//
// 可对话的判定口径：该模型在【启用且属于请求分组】的渠道上有一条
// 【成功的测速结果】（OK 且 TTFB>0）。测速探测本身就是一次最小 chat 请求，
// 能拿到 200 与首字即证明"可对话"——比人工维护"哪些模型能对话"的清单可靠，
// 也天然与模型广场展示的延迟同源（广场用的是同一份测速数据）。
//
// 选择规则：取所有可对话模型中 TTFB 最小者；并列时按模型名字典序取小，
// 保证确定性（同一时刻任意两次请求得到同一答案，便于排查与复现）。
//
// 失败语义：无测速数据源 / 当前分组没有可对话模型时返回明确错误，
// 调用方以 503 提示使用者先对渠道测速，而不是透传一条看不懂的上游错误。
func (r *Relay) resolveAutoChatModel(ctx context.Context, group string) (string, error) {
	if r.speeds == nil {
		return "", errors.New("auto 路由暂不可用：未接入测速数据（请先对渠道执行一次测速）")
	}
	if group == "" {
		group = r.group
	}

	enabled := model.ChannelStatusEnabled
	channels, err := r.channels.List(ctx, model.ChannelQuery{Group: group, Status: &enabled})
	if err != nil {
		return "", fmt.Errorf("auto 路由失败：读取渠道失败: %w", err)
	}
	serving := make(map[uint64]*model.Channel, len(channels))
	for _, ch := range channels {
		serving[ch.ID] = ch
	}

	results, err := r.speeds.Latest(ctx)
	if err != nil {
		return "", fmt.Errorf("auto 路由失败：读取测速数据失败: %w", err)
	}

	best, bestTTFB := "", 0
	for _, res := range results {
		if !res.OK || res.TTFBMS <= 0 {
			continue
		}
		ch := serving[res.ChannelID]
		if ch == nil {
			continue // 渠道不在本分组或已停用：旧测速结果不代表现在能服务
		}
		// 模型声明校验：结果可能早于渠道清单的后续修改，已被移除的模型不参与
		if len(ch.Models) != 0 && !ch.HasModel(res.Model) {
			continue
		}
		if best == "" || res.TTFBMS < bestTTFB || (res.TTFBMS == bestTTFB && res.Model < best) {
			best, bestTTFB = res.Model, res.TTFBMS
		}
	}
	if best == "" {
		return "", errors.New("auto 路由失败：当前分组没有可对话的模型（请先对渠道执行测速，且至少有一条成功记录）")
	}
	return best, nil
}

// SelectChannel 为指定模型在指定分组中选择可用渠道（不排除任何渠道）。
//
// 适用场景：管理后台的连通性测试、渠道体检等"只想知道有没有可用渠道"的调用。
// 转发路径使用 listCandidates + pickCandidate，以便跳过本次请求已失败的渠道。
//
// 参数 group 为空时回退到 Relay 的默认分组（见 listCandidates 的说明）。
func (r *Relay) SelectChannel(ctx context.Context, group, modelName string) (*model.Channel, error) {
	candidates, err := r.listCandidates(ctx, group, modelName)
	if err != nil {
		return nil, err
	}

	ch := pickCandidate(candidates, nil)
	if ch == nil {
		return nil, fmt.Errorf("%w（分组=%s, 模型=%s）", ErrNoAvailableChannel, group, modelName)
	}
	return ch, nil
}

// pickCandidate 从候选集中挑出一个渠道，跳过 excluded 中已失败的渠道。
//
// 策略：
//  1. 只考虑【最高优先级】的一层。优先级是运维表达"先用谁"的强意图
//     （如先用便宜的、再用贵的），不允许被权重跨越；
//  2. 层内按权重随机，避免流量全部压在同层第一个渠道上；
//  3. 该层被排除殆尽时，自然回落到下一优先级层（candidates 已按优先级降序）。
//
// 候选已耗尽时返回 nil（调用方据此判断"无更多可尝试渠道"）。
func pickCandidate(candidates []*model.Channel, excluded map[uint64]struct{}) *model.Channel {
	var topPriority int
	hasTop := false

	tier := make([]*model.Channel, 0, len(candidates))
	for _, ch := range candidates {
		if _, skip := excluded[ch.ID]; skip {
			continue
		}
		if !hasTop {
			topPriority = ch.Priority
			hasTop = true
		}
		// 列表已按优先级降序，遇到更低优先级即说明本层已收集完毕
		if ch.Priority != topPriority {
			break
		}
		tier = append(tier, ch)
	}

	if len(tier) == 0 {
		return nil
	}
	return weightedPick(tier)
}

// weightedPick 在同优先级的一组渠道中按权重随机挑选一个。
//
// 算法：累加权重后取一个随机数 r ∈ [0, total)，顺序累减权重，
// 首次使累计值小于 0 的渠道即中选。权重越大命中概率越高。
//
// 边界处理：若权重之和 <= 0（理论上被 Validate 拦住，兜底防止死循环），
// 退化为等概率随机。
func weightedPick(channels []*model.Channel) *model.Channel {
	if len(channels) == 1 {
		return channels[0]
	}

	total := 0
	for _, ch := range channels {
		total += ch.Weight
	}
	if total <= 0 {
		return channels[rand.IntN(len(channels))]
	}

	remaining := rand.IntN(total)
	for _, ch := range channels {
		remaining -= ch.Weight
		if remaining < 0 {
			return ch
		}
	}
	// 理论上不可达（前面必然返回），兜底返回最后一个
	return channels[len(channels)-1]
}
