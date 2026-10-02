// 本文件实现 OpenAI 兼容协议的请求转发（M2 起支持多渠道路由与故障转移）。
//
// 意图（Why）：
//
//	绝大多数客户端工具（SDK、Cursor、LobeChat 等）都按 OpenAI 协议发起请求，
//	因此把 OpenAI 兼容格式作为网关的「母语」最省事：上游若也是 OpenAI 兼容实现，
//	我们只需改写目标地址与鉴权，其余原样转发，几乎零转换成本与信息损失。
//
// 流转（Flow）：
//
//	ServeChatCompletions(w, req)
//	  ├─ 步骤1 读取请求体（复用 oai.ReadBody：限长 + 还原 body）
//	  ├─ 步骤2 探测 model 字段（oai.PeekModel）
//	  ├─ 步骤3 解析请求分组并选渠道，失败按策略换渠道重试（见 forwardWithFallback）
//	  ├─ 步骤4 构造上游请求：改写 URL / Authorization，保留 Accept 与 User-Agent
//	  ├─ 步骤5 回写状态码与响应头（过滤逐跳头）
//	  └─ 步骤6 流式拷贝响应体并逐段 Flush（SSE 关键）
//
// 扩展（Extend）：
//
//	新增端点（/v1/embeddings 等）：复用 oai 包中的协议工具与
//	  forwardWithFallback，仅需处理各自的请求体差异。
//	新增协议转换：在步骤 3 之前插入"请求体转换"，在步骤 6 处插入"响应体转换"。
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gitee.com/xiaosu4610/aqua-api/internal/corpus"
	"gitee.com/xiaosu4610/aqua-api/internal/model"
	"gitee.com/xiaosu4610/aqua-api/internal/oai"
)

// copyBufferBytes 是上游响应的拷贝缓冲区大小。
//
// 取 32KB：既能减少系统调用次数，又能让首字尽早到达客户端
// （缓冲区过大会让分片被攒住，破坏"逐字输出"的体验）。
const copyBufferBytes = 32 * 1024

// ServeChatCompletions 处理 POST /v1/chat/completions（透传 + 多渠道路由）。
//
// 参数使用标准库类型而非框架类型，目的是让 relay 包不依赖具体 Web 框架，
// 便于测试（httptest 直接调用）与将来替换框架。
func (r *Relay) ServeChatCompletions(w http.ResponseWriter, req *http.Request) {
	// ── 步骤 1：读取请求体 ──────────────────────────────────────
	// 复用 oai.ReadBody：它同时完成限长检查与 body 还原，
	// 保证鉴权中间件读过 body 后这里仍能完整读取。
	body, err := oai.ReadBody(req)
	if err != nil {
		if errors.Is(err, oai.ErrRequestTooLarge) {
			oai.WriteError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("请求体超过上限（%d 字节）", oai.MaxRequestBodyBytes),
				oai.TypeInvalidRequest, oai.CodeRequestTooLarge)
			return
		}
		oai.WriteError(w, http.StatusBadRequest, "读取请求体失败",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	// ── 步骤 2：探测 model 字段（路由与校验的输入）──────────────
	modelName, err := oai.PeekModel(body)
	if err != nil {
		if errors.Is(err, oai.ErrMissingModel) {
			oai.WriteError(w, http.StatusBadRequest, "缺少 model 字段",
				oai.TypeInvalidRequest, oai.CodeMissingModel)
			return
		}
		oai.WriteError(w, http.StatusBadRequest, "请求体不是合法的 JSON",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	// ── 步骤 2.5：基础参数边界校验（2026-09-30 新增）─────────────
	// 线上数据：按次专线的 400 里，"temperature 越界"占比最高
	// （上游严格要求 [0, 2) 半开区间，传 2.0 必被拒）。
	// 在入口统一校验，把这类可预期的用户错误变成明确中文提示，
	// 而不是透传一条上游的英文 400。
	if err := oai.ValidateChatParameters(body); err != nil {
		if errors.Is(err, oai.ErrTemperatureOutOfRange) {
			oai.WriteError(w, http.StatusBadRequest, err.Error(),
				oai.TypeInvalidRequest, "temperature_out_of_range")
			return
		}
		oai.WriteError(w, http.StatusBadRequest, "请求体不是合法的 JSON",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	// ── 步骤 3~6：转发（含失败换渠道重试）───────────────────────
	// 语料共建：命中采集清单时，把"请求原文 + 返回正文副本"的缓冲挂到请求上下文上。
	//
	// 为什么挂 context 而不是加函数参数：下面要经过 forwardWithFallback →
	// forwardChat（可能多轮重试）→ 适配器回写，逐层加参数会污染一串
	// 与本功能无关的签名；挂 context 只需在入口挂一次、在落库处取一次。
	//
	// 判定为"否"时（绝大多数请求）这里什么都不做，链路上零额外开销。
	if r.corpus != nil && r.corpusSamples != nil && r.corpus.ShouldCollect(modelName) {
		req = req.WithContext(corpus.WithRecorder(req.Context(), corpus.NewRecorder(body, corpus.DefaultMaxBytes)))
	}

	// adapter 为 nil：入站已是 OpenAI 协议，响应直接透传，无需转换。
	r.forwardWithFallback(w, req, modelName, body, nil, oai.ChatCompletionsPath)
}

// ServeEmbeddings 处理 POST /v1/embeddings（OpenAI 兼容的向量嵌入透传）。
//
// 为什么需要它：NVIDIA 免费模型里有不少 embedding / rerank / clip 类模型，
// 它们只提供 /v1/embeddings。网关若只转发对话接口，这些模型就会
// "上架了但调不通"，只能从清单里剔掉——白白浪费可用的免费算力。
//
// 实现上完全复用对话那套链路（选渠道 → 密钥池 → 重试 → 计费 → 日志），
// 唯一差别是上游路径不同。计费同样按 token：embedding 请求的 usage
// 只有 prompt_tokens，completion 为 0，公式天然成立。
func (r *Relay) ServeEmbeddings(w http.ResponseWriter, req *http.Request) {
	body, err := oai.ReadBody(req)
	if err != nil {
		if errors.Is(err, oai.ErrRequestTooLarge) {
			oai.WriteError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("请求体超过上限（%d 字节）", oai.MaxRequestBodyBytes),
				oai.TypeInvalidRequest, oai.CodeRequestTooLarge)
			return
		}
		oai.WriteError(w, http.StatusBadRequest, "读取请求体失败",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	modelName, err := oai.PeekModel(body)
	if err != nil {
		if errors.Is(err, oai.ErrMissingModel) {
			oai.WriteError(w, http.StatusBadRequest, "缺少 model 字段",
				oai.TypeInvalidRequest, oai.CodeMissingModel)
			return
		}
		oai.WriteError(w, http.StatusBadRequest, "请求体不是合法的 JSON",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	r.forwardWithFallback(w, req, modelName, body, nil, oai.EmbeddingsPath)
}

// ServeResponses 处理 POST /v1/responses（Responses 协议直连）。
//
// 与 ServeChatCompletions 的关系：两者走完全相同的转发链路（路由、密钥池、
// 重试、计费），差别只在"双方说的是哪种协议"——
//   - chat.completions：客户端说 chat，上游若是订阅账号则需转成 Responses；
//   - responses：客户端本来就说 Responses，若上游也是 Responses 则原样透传。
//
// 请求体不做"猜测式改写"：只有订阅账号（Codex）才需要归一
// （store=false / stream=true / instructions 非空），其余上游按原文转发——
// 因为我们无从判断第三方上游接受哪些字段，擅自改写反而会引入新的失败点。
func (r *Relay) ServeResponses(w http.ResponseWriter, req *http.Request) {
	body, err := oai.ReadBody(req)
	if err != nil {
		if errors.Is(err, oai.ErrRequestTooLarge) {
			oai.WriteError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("请求体超过上限（%d 字节）", oai.MaxRequestBodyBytes),
				oai.TypeInvalidRequest, oai.CodeRequestTooLarge)
			return
		}
		oai.WriteError(w, http.StatusBadRequest, "读取请求体失败",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	modelName, err := oai.PeekModel(body)
	if err != nil {
		if errors.Is(err, oai.ErrMissingModel) {
			oai.WriteError(w, http.StatusBadRequest, "缺少 model 字段",
				oai.TypeInvalidRequest, oai.CodeMissingModel)
			return
		}
		oai.WriteError(w, http.StatusBadRequest, "请求体不是合法的 JSON",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	r.forwardWithFallback(w, req, modelName, body, nil, oai.ResponsesPath)
}

// forwardTarget 描述「一次转发尝试」的完整目标：哪个渠道 + 用哪把密钥。
//
// 为什么要额外携带密钥信息：一个渠道可能挂着几百把密钥（密钥池），
// "渠道选对了"不代表"这把密钥能用"。把密钥与重试能力一起传给转发函数，
// 才能让它在密钥级失败时做出正确决策（换同渠道的另一把，而不是换渠道）。
type forwardTarget struct {
	channel *model.Channel
	// apiKey 是本次要提交给上游的密钥（可能来自密钥池，也可能是渠道自带的单密钥）。
	apiKey string
	// keyID 是密钥池内的记录 ID；0 表示单密钥模式（渠道没有密钥池）。
	keyID uint64
	// keyFailCount 是该凭据此前的连续失败次数，用于计算冷却的指数退避。
	keyFailCount int
	// keyMeta 是该凭据的账号级元数据（订阅类协议出站时需要，如 Codex 的账号标识）。
	keyMeta CredentialMeta
	// hasSpareKey 表示同渠道池内还有本次未用过的备用密钥，可用于密钥级重试。
	hasSpareKey bool
	// hasSpareChannel 表示除本渠道外还有其他候选渠道，可用于渠道级重试。
	hasSpareChannel bool
}

// forwardWithFallback 按路由策略选择渠道与密钥并转发，失败时按失败类型重试。
//
// 重试语义（M3 起细分两类失败）：
//   - 密钥级失败（401/403/402/429）：说明"这把密钥不能用"，
//     优先在同渠道的密钥池里换一把（这是密钥池的核心价值：
//     单渠道也能靠池子消化掉失效密钥，不会因一把钥匙坏掉就整体不可用）；
//   - 渠道级失败（连接失败、500/502/503/504/529）：说明"这个上游有问题"，
//     换下一个渠道；
//   - 其他 4xx（400/404 等）：请求本身有问题，换谁都无法成功，
//     【不重试】，按本站语义化错误码脱敏后回给下游（见 sanitizeUpstreamError）。
//
// 预算可配置（迁移 0037）：是否重试与重试几次由【渠道】决定，
// 模型级可单独覆盖（见 model.Channel.RetryPolicyFor）。关闭时对上游只尝试一次，
// 既不换密钥也不换渠道——适合"重复请求会产生重复扣费"的上游。
//
// 重试的硬约束：只有在【尚未向客户端写出任何内容】时才允许重试，
// 因此判定必须发生在 WriteHeader 之前——状态码一旦发出就无法撤回。
//
// 参数 adapter 为 nil 时按 OpenAI 协议原样透传；非 nil 时由适配器
// 把上游响应转换为下游协议（见 adapter.go）。
//
// 参数 upstreamPath 是上游要请求的端点路径（如 /v1/chat/completions、
// /v1/embeddings）。参数化的原因：不同下游能力对应上游不同端点，
// 但"选渠道 / 密钥池 / 重试 / 计费 / 日志"这一整套逻辑完全相同，
// 不该为了多一个端点而复制一遍转发实现。
func (r *Relay) forwardWithFallback(w http.ResponseWriter, req *http.Request, modelName string, body []byte, adapter Adapter, upstreamPath string) {
	// 注意：这里【不】改写请求体。是否注入 stream_options.include_usage 取决于
	// 本次选中的渠道（见 injectStreamUsageFor：渠道扩展参数 > 全局常量），
	// 而渠道是在下面的重试循环里逐轮选出来的，因此改写发生在循环内、
	// 每次都从原始 body 派生，保证"同一次请求的各次尝试行为一致"。

	// 解析本次请求分组：【只解析一次】，之后整条链路复用同一个值。
	//
	// 取值来自令牌（见 reqctx.Group），令牌未指定时回退到 Relay 默认分组。
	// 渠道选择、失败重试换渠道、计费都必须使用这个值——
	// 若中途按不同来源重新解析，就会出现"按 A 分组选渠道、按 B 分组计费"的错账。
	group := r.groupFromContext(req.Context())

	// 一次性取出候选集：同一次请求内的多次重试都基于它挑选，避免每次重试都查库
	candidates, err := r.listCandidates(req.Context(), group, modelName)
	if err != nil {
		// 仓储查询失败：不向客户端暴露细节，但必须留痕，
		// 否则"用户报 500 却查不到原因"时无从下手。
		slog.Error("查询候选渠道失败", "error", err, "model", modelName, "group", group)
		writeAdaptedError(w, adapter, http.StatusInternalServerError, "网关内部错误",
			oai.TypeServer, oai.CodeInternal)
		return
	}
	if len(candidates) == 0 {
		// 一个可用渠道都没有：属于"配置/容量"问题。
		// 用 503（而非 500）表达"暂时无可用后端"，客户端稍后重试可能成功。
		//
		// 注意：这种情况也记一条日志——"配置漏了模型"是常见事故，
		// 若不留痕，站长只能看到用户报错却查不到原因。
		//
		// 区分两种情形（2026-09-30 新增，源于线上 503 刷屏事故）：
		//   · 模型在【其他分组】有渠道、只在当前分组没有 → 配置了"分组可见性"
		//     但没有给该分组接渠道（典型：收费模型对免费分组可见却路由不到），
		//     应明确告诉使用者"这个分组没有开通该模型"，而不是笼统的"无可用渠道"；
		//   · 模型在全站任何分组都没有渠道 → 才是真正的"无可用渠道"。
		// 前者属于可见性配置错误，后者属于容量问题，处置动作完全不同。
		anyChannel := r.channelCountForModel(req.Context(), modelName)
		message := "当前没有可用的上游渠道能处理该模型"
		if anyChannel > 0 {
			message = "当前分组未开通该模型（模型在其他分组可用），请检查令牌分组或联系管理员"
		}
		r.recordUsage(req.Context(), usageEntry{
			UserID:     identityFromRequest(req.Context()).UserID,
			TokenID:    identityFromRequest(req.Context()).TokenID,
			Group:      group,
			Model:      modelName,
			IsStream:   oai.PeekStream(body),
			StatusCode: http.StatusServiceUnavailable,
			ErrorText:  "无可用渠道（分组=" + group + "）",
		})
		writeAdaptedError(w, adapter, http.StatusServiceUnavailable,
			message, oai.TypeServer, oai.CodeNoAvailableChannel)
		return
	}

	excludedChannels := make(map[uint64]struct{}) // 本次请求已放弃的渠道
	usedKeys := make(map[uint64]struct{})         // 本次请求已用过的密钥（避免重复撞同一把）

	// 会话标识：客户端若带约定请求头，则整轮重试都尝试粘在同一把凭据上；
	// 未携带时为空串，调度退化为无粘性（不做任何猜测）。
	sessionHash := stickySessionKey(req)
	keyCtx := withCredentialSession(req.Context(), sessionHash)

	// 两套独立的尝试预算（重要，别合并成一个）：
	//
	//   · channelAttempts：渠道级失败（连不上、5xx）的预算 = 本次请求重试策略里的次数。
	//     渠道级失败每次都要重新建连，代价高，必须严格限制，否则故障时延迟被放大。
	//   · 密钥级失败不走渠道预算，而是由 keyAttempts 单独计量，上限 maxKeyLevelAttempts。
	//
	// 为什么密钥级需要更大的预算：一个渠道的密钥池可能来自几百个不同账号
	// （例如 NVIDIA NIM 的免费额度池），而每个账号的模型授权是不同的——
	// 同一个模型在 A 账号"没有权限"、在 B 账号完全可用。
	// 此时"换一把密钥"的成本极低（同一上游、复用连接、上游是立即拒绝的），
	// 所以值得多试几把；若沿用 3 次的渠道预算，绝大多数请求会白跑一趟。
	channelAttempts := 0
	// 重试策略在请求开始时【一次性定下】：由候选清单里优先级最高的那个渠道
	// （即路由的首选渠道，见 listCandidates 的排序约定）代表本次请求，
	// 结合请求的模型名解析出"是否重试 + 最多尝试几个渠道"。
	//
	// 为什么不在每轮按当前渠道重算：那样每换一个渠道数字就会变，
	// 站长配的"重试 2 次"可能实际打出 3 次，行为不可预期、事后也无法解释。
	// 让首选渠道代表本次请求，语义稳定且与"优先级越高越先被选中"的直觉一致。
	//
	// 注意：策略关闭时预算压到 1，同时禁用"换密钥/换渠道"两条重试路径——
	// 后者是 keyFailureCredential / keyFailureEntitlement 分支判断是否重试的依据，
	// 只把预算改成 1 是不够的（那是渠道级预算，密钥级分支不看它）。
	retryPolicy := candidates[0].RetryPolicyFor(modelName)
	channelBudget := retryPolicy.MaxAttempts
	if !retryPolicy.Enabled {
		channelBudget = 1
	}
	// lastFailure 记录"最后一次上游失败"，用于所有重试耗尽后写入本站调用日志
	var lastFailure upstreamFailure
retryLoop:
	for keyAttempts := 1; keyAttempts <= maxKeyLevelAttempts; keyAttempts++ {
		ch := pickCandidate(candidates, excludedChannels)
		if ch == nil {
			// 候选渠道已全部放弃，退出循环统一报错
			break
		}

		cred, ok, hasSpareKey := r.resolveChatCredential(keyCtx, ch, usedKeys,
			credentialScope{Group: group, Model: modelName})
		if !ok {
			// 该渠道当前没有可用凭据（池内全部被禁用、冷却中、限速用满或额度用尽）：
			// 直接放弃这个渠道，避免白白消耗一次尝试预算。
			excludedChannels[ch.ID] = struct{}{}
			continue
		}
		if cred.KeyID != 0 {
			usedKeys[cred.KeyID] = struct{}{}
		}

		target := forwardTarget{
			channel:         ch,
			apiKey:          cred.Value,
			keyID:           cred.KeyID,
			keyFailCount:    cred.FailCount,
			keyMeta:         cred.Meta,
			hasSpareKey:     retryPolicy.Enabled && hasSpareKey,
			hasSpareChannel: retryPolicy.Enabled && r.hasOtherChannel(candidates, excludedChannels, ch.ID),
		}

		// 占用在途计数（供 least_in_flight 使用）：与下方的 releaseKey 成对，
		// 覆盖本轮从"选定凭据"到"响应结束"的整个区间。
		// 只有确实占上才释放（acquireKey 失败时 releaseKey 是空操作，避免计数被减成负数）。
		acquired := r.acquireKey(keyCtx, target.keyID)
		// 按本轮的渠道决定是否注入 include_usage（见 injectStreamUsageFor）。
		// 每次都从原始 body 派生，因此同一请求的各次尝试只在渠道不同时才不同。
		attemptBody := withStreamUsageOption(body, injectStreamUsageFor(ch))
		outcome := r.forwardChat(w, req, target, group, modelName, attemptBody, adapter, upstreamPath, &lastFailure)
		// 归还本轮的在途占用：无论成功、换密钥还是换渠道，都必须释放，
		// 否则 in_flight 只增不减，least_in_flight 会逐步失去参考价值。
		r.releaseKey(target.keyID, acquired)
		switch outcome {
		case forwardResponded:
			return
		case forwardRetryKey:
			// 密钥级失败：保留该渠道，下一轮会从它的池里换一把密钥
			continue
		case forwardRetryChannel:
			excludedChannels[ch.ID] = struct{}{}
			channelAttempts++
			if channelAttempts >= channelBudget {
				// 预算耗尽：或按渠道/模型策略已用完次数，或上游整体故障时不该无限试下去
				break retryLoop
			}
			continue
		}
	}

	// 所有尝试都用尽：把上游最后一次失败的原因写进本站日志，并向客户端回本站定制错误。
	//
	// 为什么不再透传上游原始响应：上游错误体里含上游厂商名、账号标识与原始错误码，
	// 透传会把"我们用了哪家上游、渠道怎么组织"暴露给下游（站长已确认采用语义化脱敏）。
	// 但上游的真实原因必须留下——它正是排障的关键证据（"该模型在池内所有账号上都没有
	// 授权"这类判断只能靠它），因此只写进本站调用日志，绝不出网关。
	if lastFailure.status != 0 {
		message := extractUpstreamErrorMessage(lastFailure.body)

		r.recordUsage(req.Context(), usageEntry{
			UserID:     identityFromRequest(req.Context()).UserID,
			TokenID:    identityFromRequest(req.Context()).TokenID,
			Group:      group,
			Model:      modelName,
			IsStream:   oai.PeekStream(body),
			StatusCode: lastFailure.status,
			ErrorText:  truncateReason(upstreamErrorLogText(lastFailure.status, message)),
		})

		// 无论直通还是转换路径，一律回本站脱敏错误码。
		writeSanitizedUpstreamError(w, adapter, lastFailure.status)
		return
	}

	// 没有任何可透传的上游响应（例如候选渠道为空、全部连不上）：
	// 记一条日志（channel_id 为 0，因为没有一个渠道成功完成会话）
	r.recordUsage(req.Context(), usageEntry{
		UserID:     identityFromRequest(req.Context()).UserID,
		TokenID:    identityFromRequest(req.Context()).TokenID,
		Group:      group,
		Model:      modelName,
		IsStream:   oai.PeekStream(body),
		StatusCode: http.StatusBadGateway,
		ErrorText:  "所有候选渠道均请求失败",
	})
	writeAdaptedError(w, adapter, http.StatusBadGateway, "所有候选渠道均请求失败",
		oai.TypeServer, oai.CodeUpstreamRequestFailed)
}

// resolveChatKey 为一次转发解析出要使用的上游凭据。
//
// 返回：凭据值（API Key 或 OAuth access_token）、池内 ID（单密钥模式为 0）、
// 该凭据当前的连续失败次数（供冷却退避使用）、是否解析成功、同渠道是否还有备用凭据。
//
// 四种情形：
//  1. 渠道配置了凭据池 → 按渠道策略从"启用且本次未用过"的凭据中挑一条；
//  2. 挑中的是 OAuth 凭据且即将过期 → 先刷新再使用；
//  3. 渠道没有凭据池（历史数据） → 使用渠道自带的单密钥，保持向后兼容；
//  4. 池内凭据本次已全部试过或当前全部不可用 → 返回 ok=false，让上层换渠道。
//
// 容错：凭据池查询失败时不阻断转发，而是退回单密钥——
// 统计能力不应该成为转发链路上的单点故障。
func (r *Relay) resolveChatKey(ctx context.Context, ch *model.Channel, used map[uint64]struct{}, scope credentialScope) (string, uint64, int, bool, bool) {
	cred, ok, hasSpare := r.resolveChatCredential(ctx, ch, used, scope)
	return cred.Value, cred.KeyID, cred.FailCount, ok, hasSpare
}

// resolvedCredential 是一次转发中被选中的凭据，附带它的账号级元数据。
//
// 为什么要把元数据一起带出来：订阅类协议（Codex）出站时必须带 chatgpt-account-id，
// 而该值属于"这一条凭据"而不是渠道。若不带出来，上层只能重新查一次库。
type resolvedCredential struct {
	// Value 是可直接提交给上游的凭据值（API Key 或已刷新的 access_token）。
	Value string
	// KeyID 是池内记录 ID；0 表示单密钥模式。
	KeyID uint64
	// FailCount 是该凭据当前的连续失败次数（供冷却退避使用）。
	FailCount int
	// Meta 是账号级元数据（非订阅类凭据为零值）。
	Meta CredentialMeta
}

// resolveChatCredential 与 resolveChatKey 同源，额外回传账号级元数据。
//
// 参数 scope 携带本次请求的分组与模型：凭据可声明"只服务某些分组 / 某些模型"
// （迁移 0038），因此挑凭据前必须按它过滤（见 filterUsableKeysForRequest）。
func (r *Relay) resolveChatCredential(ctx context.Context, ch *model.Channel, used map[uint64]struct{}, scope credentialScope) (resolvedCredential, bool, bool) {
	if r.keys != nil {
		pool, poolErr := r.keys.ListUsable(ctx, ch.ID)
		if poolErr != nil {
			// 查询失败不阻断转发（见上方"容错"说明，退回单密钥继续），
			// 但必须留痕：否则 DB 抖动期间"密钥池配了却从没被选中"完全无从排查，
			// 降级过程对站长彻底不可见。
			slog.Error("读取凭据池失败，本次退回渠道单密钥",
				"error", poolErr, "channel_id", ch.ID, "model", scope.Model)
		}
		if poolErr == nil && len(pool) > 0 {
			sessionHash := credentialSessionFrom(ctx)
			// 循环而非单次挑选：OAuth 凭据可能因刷新失败而不可用，
			// 此时应换池内下一条，而不是让整个请求失败。
			for attempt := 0; attempt < maxCredentialAttempts; attempt++ {
				available := make([]*model.ChannelKey, 0, len(pool))
				for _, k := range pool {
					if _, dup := used[k.ID]; dup {
						continue
					}
					available = append(available, k)
				}
				if len(available) == 0 {
					return resolvedCredential{}, false, false
				}

				// 先按"当前是否可用"过滤（状态/冷却/限速/余额/额度窗口），再按本次请求的
				// 分组与模型过滤（凭据可声明只服务某些分组/模型），最后交给策略挑选。
				//
				// 为什么要在这里提前过滤而不是只靠 SelectKey：下面的 hasSpareKey
				// 直接由本切片的长度推断，若不过滤，一批"余额已耗尽"或"不支持该模型"的
				// 凭据会让 hasSpareKey 误判为 true，进而触发无意义的密钥级重试。
				usable := filterUsableKeysForRequest(available, time.Now(), scope)
				if len(usable) == 0 {
					// 过滤后无可用凭据（全部冷却/限速/禁用/余额耗尽/额度用满，
					// 或都不服务本次的分组/模型）：让上层换渠道
					return resolvedCredential{}, false, false
				}

				// 策略选择：五策略择优 + 会话粘性（内部会再做一次幂等的可用性过滤）。
				picked, err := r.SelectKey(ctx, ch, usable, sessionHash)
				if err != nil {
					// 这是"故障"而非"业务上无可用凭据"：不记日志的话，
					// 它会和正常冷却/额度耗尽混在一起，最终只表现为一句
					// "所有候选渠道均请求失败"，把系统自身的问题抹平成上游问题。
					slog.Error("选择凭据失败", "error", err,
						"channel_id", ch.ID, "model", scope.Model)
					return resolvedCredential{}, false, false
				}

				now := time.Now()
				// 记录使用时间：失败不影响本次转发（这是展示性数据，不是控制流）
				_ = r.keys.MarkUsed(ctx, picked.ID, now)

				value := picked.CredentialValue()
				if r.oauth != nil && picked.NeedsRefresh(now) {
					fresh, refreshErr := r.oauth.EnsureFresh(ctx, picked)
					if refreshErr != nil {
						// 刷新失败：按渠道策略处置（只冷却不摘除时不会摘掉凭据），
						// 标记为本次已用过并换下一条凭据
						r.markCredentialHardFailure(ctx, ch, picked.ID, "刷新令牌失败: "+refreshErr.Error())
						used[picked.ID] = struct{}{}
						continue
					}
					value = fresh
				}

				if strings.TrimSpace(value) == "" {
					// 凭据内容为空（配置错误）：跳过并计一次失败，
					// 否则它会一直占着池子却永远发不出请求。
					// 同样受渠道策略约束（见 markCredentialHardFailure）。
					r.markCredentialHardFailure(ctx, ch, picked.ID, "凭据内容为空")
					used[picked.ID] = struct{}{}
					continue
				}

				// 按需记录 RPM 用量（写库失败不影响转发）。
				if picked.RPMLimit > 0 {
					_ = r.keys.RecordRequest(ctx, picked.ID, now, rpmWindow)
				}
				return resolvedCredential{
					Value:     value,
					KeyID:     picked.ID,
					FailCount: picked.FailCount,
					Meta: CredentialMeta{
						AccountID: picked.AccountID,
						PlanType:  picked.PlanType,
					},
				}, true, len(usable) > 1
			}
			return resolvedCredential{}, false, false
		}
	}

	if ch.APIKey == "" {
		return resolvedCredential{}, false, false
	}
	// 单密钥模式：没有池内记录，也就没有账号级元数据；
	// 订阅类协议会退化为"从 access_token 的 JWT 里现取账号标识"（见 applyCredentialHeaders）。
	return resolvedCredential{Value: ch.APIKey}, true, false
}

// maxCredentialAttempts 是单次请求内最多尝试的凭据条数。
//
// 取 3：既能在"个别 OAuth 账号刷新失败"时快速换到可用凭据，
// 又不会因为池里存在大量坏凭据而让单个请求长时间打转
// （每个坏凭据都要等一次刷新超时，代价不低）。
const maxCredentialAttempts = 3

// maxKeyLevelAttempts 是单次请求内最多尝试的凭据轮数（凭据级重试预算）。
//
// 取值 8 的权衡：
//   - 一个渠道挂几百把密钥时，个别密钥被限流（429）或临时失效是常态，
//     多试几把能显著提升成功率，而这些失败都是毫秒级的、不消耗算力；
//   - 但不能无限试：上游整体故障时会把延迟放大到不可接受。
//
// 注意它【不适用于"该账号没有这个模型"】这类失败：实测本部署的
// 500 把免费密钥授权完全一致（3 把取样密钥对 8 个模型结论逐一相同），
// 所以"某模型在 A 账号没有"就等于"在整池都没有"，换密钥毫无意义，
// 只会白白多花几秒。这类失败由 keyFailureEntitlement 单独处理：
// 只在"还有别的渠道"时换渠道，否则立刻按本站语义化错误码脱敏回下游。
const maxKeyLevelAttempts = 8

// keyFailureKind 表示一次上游失败与"凭据"的关系，决定是否可以换密钥重试。
type keyFailureKind int

const (
	// keyFailureNone 与凭据无关（如请求体有误、上游 5xx）：
	// 换密钥没用；上游 5xx 可换渠道重试（见 isRetryableStatus），
	// 请求体有误则按本站语义化错误码脱敏回下游。
	keyFailureNone keyFailureKind = iota
	// keyFailureCredential 凭据本身不可用（失效 / 受限 / 被限流）：
	// 池内换一把很可能成功，值得重试。
	keyFailureCredential
	// keyFailureEntitlement 该凭据所在账号没有这个模型 / 无权访问：
	// 池内账号同质时换密钥无意义，只在还有别的渠道时才换渠道重试，
	// 否则立刻收手、按本站语义化错误码脱敏回下游（不再等几秒拿一个含糊的失败）。
	keyFailureEntitlement
)

// upstreamFailure 记录"最后一次上游失败"的原始信息。
//
// 为什么需要它：当所有重试都用尽时，上游的真实原因（状态码 + 响应体片段）必须留下，
// 否则管理员无从判断到底是模型不存在、账号没权限、还是上游故障。
// 但这份原始信息只写进本站调用日志，绝不透传给下游——下游拿到的是本站语义化错误码
// （见 upstream_error.go 的 sanitizeUpstreamError）。
type upstreamFailure struct {
	status int
	body   []byte
}

// keyLevelPeekBytes 是判定"凭据被拒"时窥探响应体的字节数。
//
// 取 4KiB：各类上游的错误说明都很短，足够覆盖；同时避免为判断读入大响应。
const keyLevelPeekBytes = 4 << 10

// keyLevelRejectionMarkers 是"上游明确指出这把凭据/这个账号不可用"的文本特征。
//
// 为什么需要文本判据：有些上游用 400/404 表达"该账号没有这个模型"
// （NVIDIA NIM 返回 404 + application/problem+json，内容形如
// "Function '...': Not found for account '...'"）。
// 只看状态码会把这类"换把密钥就能成功"的情形误判为"请求本身有错"。
var keyLevelRejectionMarkers = []string{
	"not found for account",
	"no permission",
	"permission denied",
	"does not have access",
	"you do not have access",
	"not authorized",
	"unauthorized",
	"invalid api key",
	"api key is invalid",
	"incorrect api key",
	"account is not authorized",
	"quota",
	"rate limit",
	"exceeded",
}

// bodyWithPrefix 把"已读走的前缀"与"剩余部分"重新组合为 ReadCloser。
//
// 用途：判断错误类型时需要读一小段响应体，读完之后必须把响应体还原，
// 否则后续读取（如取错误原因写日志）会读到残缺内容。
type bodyWithPrefix struct {
	io.Reader
	closer io.Closer
}

// Close 关闭底层响应体。
func (b bodyWithPrefix) Close() error { return b.closer.Close() }

// peekBody 读取响应体开头的一段并还原它，返回读到的内容。
func peekBody(resp *http.Response, limit int) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}

	buf := make([]byte, limit)
	n, err := io.ReadFull(resp.Body, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	peek := buf[:n]

	// 还原：先用已读到的前缀，再接上尚未读完的部分
	resp.Body = bodyWithPrefix{
		Reader: io.MultiReader(bytes.NewReader(peek), resp.Body),
		closer: resp.Body,
	}
	return peek, nil
}

// extractUpstreamErrorMessage 从上游错误体里取一句可读说明。
//
// 兼容两种常见形态：
//   - OpenAI 风格：{"error":{"message":"..."}}
//   - RFC7807 问题详情：{"title":"Not Found","detail":"Function ...: Not found for account ..."}
//     （NVIDIA NIM 用的就是这种）
//
// 这样"上游到底说了什么"才能如实呈现在错误信息里。
func extractUpstreamErrorMessage(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if message := extractErrorMessage(raw); message != "" && message != "上游请求失败" {
		return message
	}

	var problem struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &problem); err == nil {
		if strings.TrimSpace(problem.Detail) != "" {
			return problem.Detail
		}
		if strings.TrimSpace(problem.Title) != "" {
			return problem.Title
		}
	}
	return ""
}

// classifyKeyFailure 判断这次上游失败与"凭据"是什么关系。
//
// 判据分三类（顺序不能反）：
//  1. 401 / 403 / 402 / 429：凭据本身不可用（失效 / 受限 / 被限流）→ 值得换密钥；
//  2. 400 / 404 且响应体文本表明"该账号无权访问/没有这个模型"
//     → 属于授权范围问题，同质账号池里换密钥无意义；
//  3. 其余（请求体有误、5xx 等）→ 与凭据无关。
//
// 返回 snippet 供"所有重试都用尽"时写入本站调用日志（不回流给下游），
// 以及供失败处置（冷却 / 摘除）判断响应体是否明确表示凭据永久无效。
func (r *Relay) classifyKeyFailure(resp *http.Response) (keyFailureKind, string, []byte) {
	if resp == nil {
		return keyFailureNone, "", nil
	}
	if isKeyLevelFailure(resp.StatusCode) {
		// 需要读取响应体：仅凭状态码无法区分"临时封禁"与"永久吊销"，
		// 二者对应"冷却"与"摘除"两种完全不同的处置（见 classifyCredentialFailure）。
		peek, _ := peekBody(resp, keyLevelPeekBytes)
		return keyFailureCredential, fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode), peek
	}
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		return keyFailureNone, "", nil
	}

	peek, err := peekBody(resp, keyLevelPeekBytes)
	if err != nil || len(peek) == 0 {
		// 读不到内容就无法判定：按"请求本身的问题"处理，
		// 宁可少重试，也不要因为一次读取失败而做无谓的密钥轮换。
		return keyFailureNone, "", nil
	}

	lowered := strings.ToLower(string(peek))
	for _, marker := range keyLevelRejectionMarkers {
		if strings.Contains(lowered, marker) {
			return keyFailureEntitlement, fmt.Sprintf("上游返回 HTTP %d，并指出该账号无权访问（命中 %q）",
				resp.StatusCode, marker), peek
		}
	}
	return keyFailureNone, "", nil
}

// saveFailure 记录一次上游失败响应，供后续"所有重试都用尽"时写入本站日志。
//
// snippet 是已读出的响应体片段（由调用方通过 peekBody 取得并还原响应体）；
// 允许为空：状态码本身才是决定"回给下游哪个语义化错误码"的关键，
// 响应体只是排障证据——上游返回空体时不能因此丢掉状态码。
func saveFailure(lastFailure *upstreamFailure, resp *http.Response, snippet []byte) {
	if lastFailure == nil || resp == nil {
		return
	}
	lastFailure.status = resp.StatusCode
	if len(snippet) > 0 {
		lastFailure.body = snippet
	}
}

// truncateReason 截断失败原因，避免超长错误信息撑大数据库字段。
func truncateReason(reason string) string {
	const maxLength = 200
	if len(reason) <= maxLength {
		return reason
	}
	return reason[:maxLength]
}

// hasOtherChannel 判断"放弃当前渠道后"是否还有其他候选渠道。
//
// 实现方式：临时把当前渠道加入排除集后再挑一次（仅探测，不真正使用结果）。
// 之所以需要它：它决定"这次失败还值不值得换渠道再试"——
// 若已无退路（或该渠道/模型关了重试），就直接收手，把上游错误按本站
// 语义化错误码脱敏回下游，而不是白白多花几秒再失败。
func (r *Relay) hasOtherChannel(candidates []*model.Channel, excluded map[uint64]struct{}, currentID uint64) bool {
	probe := make(map[uint64]struct{}, len(excluded)+1)
	for id := range excluded {
		probe[id] = struct{}{}
	}
	probe[currentID] = struct{}{}
	return pickCandidate(candidates, probe) != nil
}

// acquireKey 记录在途占用（in_flight + 1），返回是否真的占上了。
//
// 返回值存在的意义（配对释放）：失败（keyID 为 0、调度仓储故障、ctx 已取消）
// 时若仍调用 releaseKey，就会凭空减掉一次别人的 in_flight——
// 计数被减成负数后，least_in_flight 会把该密钥误判成"最空闲"，
// 反而被优先选中。因此"没占上就不要放"。
func (r *Relay) acquireKey(ctx context.Context, keyID uint64) bool {
	if r.keys == nil || keyID == 0 {
		return false
	}
	return r.keys.Acquire(ctx, keyID) == nil
}

// releaseKey 释放在途占用（in_flight - 1）；acquired 为 false 时直接跳过。
//
// 刻意忽略错误：这是调度统计用途，失败不应影响对客户端的响应。
// 实现保证重复释放安全（不会把计数减到负数），因此调用方无需担心重复调用。
//
// 使用独立 context 而非请求 context：响应写完后请求可能已被取消（客户端断开），
// 若沿用请求 context，这次释放会失败并让 in_flight 残留。
func (r *Relay) releaseKey(keyID uint64, acquired bool) {
	if !acquired || r.keys == nil || keyID == 0 {
		return
	}
	_ = r.keys.Release(context.Background(), keyID)
}

// markKeySuccess 记录一次密钥成功（清零连续失败计数并解除冷却）。
func (r *Relay) markKeySuccess(ctx context.Context, keyID uint64) {
	if r.keys == nil || keyID == 0 {
		return
	}
	_ = r.keys.MarkSuccess(ctx, keyID)
}

// forwardOutcome 描述一次转发的结局，用于决定后续重试方向。
type forwardOutcome int

const (
	// forwardResponded 表示已向客户端回写响应（含上游返回错误码的情况）。
	//
	// 重要：一旦进入该状态就【不能】再重试——HTTP 状态码已经发出，无法撤回。
	forwardResponded forwardOutcome = iota
	// forwardRetryKey 表示密钥级失败且同渠道还有备用密钥：应换密钥重试。
	forwardRetryKey
	// forwardRetryChannel 表示渠道级失败且还有其他候选渠道：应换渠道重试。
	forwardRetryChannel
)

// forwardChat 把请求转发到指定目标（渠道 + 密钥），并把上游响应回写给客户端。
//
// 返回值表示结局，供上层决定重试方向（见 forwardOutcome）。
//
// 参数 lastFailure 非 nil 时，会把"凭据级失败"的上游响应原样记进去，
// 供上层在重试全部用尽后写入本站调用日志（上游真实原因只留给站长排障）。
//
// 参数 group 是本次请求的分组，由 forwardWithFallback 解析一次后透传：
// 本函数只用它来记录用量（计费按同一分组进行），不再自行解析，避免串组。
//
// 参数 upstreamPath 为上游端点路径，由调用方按下游能力指定。
func (r *Relay) forwardChat(w http.ResponseWriter, req *http.Request, target forwardTarget, group, modelName string, body []byte, adapter Adapter, upstreamPath string, lastFailure *upstreamFailure) forwardOutcome {
	// 记录起始时间用于计算耗时（写入调用日志）
	start := time.Now()
	ch := target.channel

	// 解析"渠道级模型映射"：把对外模型名改写为上游模型名。
	//
	// 只有命中映射且确实改变了模型名时才会重建请求体（modelRewritten=true）：
	// 未命中时 outboundBody 就是入参 body 本身，逐字节原样透传，行为与引入映射前一致。
	// 上游模型名同时用于请求体、路径模板（{model}/{deployment}）与调用日志。
	upstreamModel, outboundBody, modelRewritten := r.resolveUpstreamModel(req.Context(), ch.ID, modelName, body)

	// 取渠道类型规格，并按该类型决定"怎么发这个请求"：解析规格 → 请求体协议转换
	// （Anthropic / Gemini 会改写请求体）→ 组装 URL / 请求头（含类型专属路径、
	// 查询参数与鉴权）。type_key 为空的渠道回退为 OpenAI 兼容，行为与旧实现一致。
	wantStream := oai.PeekStream(body)
	spec, outBody, built, prepareErr := prepareChannelUpstreamFor(
		ch, target.apiKey, upstreamModel, upstreamPath, outboundBody,
		upstreamForwardHeaders(req.Header), wantStream, target.keyMeta)
	if prepareErr != nil {
		if errors.Is(prepareErr, errRequestBodyConversion) {
			// 请求体无法转换为上游协议（如工具类型映射不了）：属调用方请求问题，
			// 换渠道也不会成功，直接回 400 让使用者自助修正。
			writeAdaptedError(w, adapter, http.StatusBadRequest, prepareErr.Error(),
				oai.TypeInvalidRequest, "request_conversion_failed")
			return forwardResponded
		}
		// 组装失败（如渠道与类型都没提供地址）：属于该渠道的配置问题。
		// 此时【尚未写出任何响应】，可安全换下一个渠道重试；
		// 若所有渠道都用尽，由上层统一回 502。
		return forwardRetryChannel
	}

	// 用请求 context：客户端断开时自动取消上游请求，避免无谓的上游消耗
	upReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, built.URL, bytes.NewReader(outBody))
	if err != nil {
		// 请求构造失败属于网关侧问题，未向上游发出请求，可换渠道重试
		return forwardRetryChannel
	}
	// 请求头由构建器给出：只设置必要的头，【绝不复用客户端的 Authorization】——
	// 客户端带的是本网关的令牌，上游需要的是渠道密钥，二者混用会导致
	// 上游鉴权失败，并把网关令牌泄露给第三方上游。
	upReq.Header = built.Header

	resp, err := r.client.Do(upReq)
	if err != nil {
		// 连接层面失败（超时、连接被拒、TLS 失败）：未写出任何响应，可安全重试。
		//
		// 刻意【不】记为密钥失败：连接失败与密钥无关，
		// 若计入失败次数，几次网络抖动就会把池里的好密钥误杀干净。
		//
		// 也不在此记日志：多次重试会产生多条记录，把请求数统计放大；
		// 最终失败会在 forwardWithFallback 的统一出口处记录一条。
		return forwardRetryChannel
	}
	defer func() { _ = resp.Body.Close() }()

	// ── 失败分流（关键：决定"换密钥"、"换渠道"还是就此收手）────
	switch kind, _, snippet := r.classifyKeyFailure(resp); kind {
	case keyFailureCredential:
		// 凭据不可用（失效/受限/被限流）：按失败类型决定"冷却"还是"摘除"。
		//   - 429 走冷却、401/403/402 走长冷却（都【不】摘除，等待自动恢复）；
		//   - 仅当上游明确表示凭据永久无效（如已吊销）才摘除。
		// 注意：升级为 channel_keys 的临时状态，绝不因一次 429 把好凭据移出池子。
		r.applyCredentialFailure(req.Context(), ch, target.keyID, target.keyFailCount, resp.StatusCode, snippet)
		// 留存响应，供所有重试用尽后写入本站调用日志（不再透传给下游）
		saveFailure(lastFailure, resp, snippet)

		if target.hasSpareKey {
			// 同渠道还有别的密钥：丢弃本次响应，换一把密钥重试
			drainAndClose(resp)
			return forwardRetryKey
		}
		if target.hasSpareChannel {
			drainAndClose(resp)
			return forwardRetryChannel
		}
		// 已无退路（或该渠道/模型已关闭重试）：落到下方统一脱敏出口

	case keyFailureEntitlement:
		// 该账号没有这个模型 / 无权访问：不是密钥坏了，而是"这个模型不在可用范围内"。
		//
		// 为什么不换密钥重试：实测本部署的密钥池来自同质的免费账号，
		// 授权集合完全一致，换多少把结果都一样，只会白白多花几秒。
		// 因此只在"还有别的渠道"时才继续；否则落到下方统一脱敏出口。
		saveFailure(lastFailure, resp, snippet)

		if target.hasSpareChannel {
			// 但另一个渠道可能是别的上游，值得一试
			drainAndClose(resp)
			return forwardRetryChannel
		}
		// 无退路（或该渠道/模型已关闭重试）：落到下方统一脱敏出口

	default:
		if isRetryableStatus(resp.StatusCode) {
			// 上游 5xx / 超时属渠道级故障，但也给本次使用的凭据一个短冷却：
			// 故障期间不要反复把同一把凭据推到上游。冷却会自动到期，不改变凭据状态。
			r.applyCredentialFailure(req.Context(), ch, target.keyID, target.keyFailCount, resp.StatusCode, nil)
			// 留存本次失败：重试预算耗尽时用它决定回给下游的语义化错误码
			// （上游 5xx → 本站 503），否则只能退化成含义模糊的 502。
			//
			// 这里才补读响应体：classifyKeyFailure 对 5xx 不读体（它与凭据无关），
			// 但 5xx 恰是最需要留下证据的一类（"上游整体挂了"还是"该模型不存在"）。
			// 读取上限 4KiB，且本响应随后就会被 drainAndClose 或整段读走，不会多占内存。
			peek, _ := peekBody(resp, keyLevelPeekBytes)
			saveFailure(lastFailure, resp, peek)
		}
		if isRetryableStatus(resp.StatusCode) && target.hasSpareChannel {
			// 上游故障/过载，且还有别的渠道可试
			drainAndClose(resp)
			return forwardRetryChannel
		}
	}

	// 上游协议入站转换：Anthropic / Gemini / Codex 渠道的响应需改写回内部 OpenAI 协议
	// （非流式整体改写、流式逐事件转换、错误体改写为 OpenAI 错误体）。
	// 其余上游此调用为空操作，既有回写路径完全不受影响。
	//
	// "下游是否本身就用 Responses"由入口路径判定：只有 /v1/responses 那条入口
	// 会传入 ResponsesPath。这样判断的依据是"客户端说了什么语言"，
	// 而不是"上游是谁"——后者无法区分同一渠道上的两种客户端。
	downstreamResponses := upstreamPath == oai.ResponsesPath
	if err := normalizeUpstreamResponseFor(spec, resp, wantStream, downstreamResponses); err != nil {
		// 读取/改写上游响应失败：此时响应头尚未发出，可换渠道重试
		drainAndClose(resp)
		return forwardRetryChannel
	}

	// 响应方向的模型名回写：把上游回包的 model 改回对外名（modelName）。
	//
	// 位置很关键：必须在 normalizeUpstreamResponse 之后——此时响应体已被统一为
	// 内部 OpenAI 形态，无论下游是 OpenAI 直通还是 Anthropic/Gemini 适配器，
	// 都能看到正确的对外模型名。
	//
	// 仅在请求侧确实改写时才回写：无映射时响应体逐字节保持上游原样。
	// 流式为逐行增量改写（绝不整段缓冲），非流式为整体 JSON 改写。
	if modelRewritten {
		applyResponseModelRewrite(resp, wantStream, modelName)
	}

	// ── 步骤 5~6：回写响应 ──────────────────────────────────────
	// 同时把内容喂给抓取器，用于事后解析 usage（token 数）。
	sniffer := newUsageSniffer()

	// 上游返回错误状态：绝不把上游的错误码与错误原文透传给下游。
	//
	// 走到这里意味着"本次尝试已判定不再重试"——换密钥 / 换渠道的分支都在上方提前
	// 返回了，所以这里是上游错误的最终出口，也是脱敏的唯一入口。处置三步：
	//   1. 读走错误体：其一，读出上游真实原因写进本站日志（"读取它的报错码"）；
	//      其二，读完才能复用 TCP 连接，避免每次错误都重新建连；
	//   2. 按本站语义化错误码回写下游（见 sanitizeUpstreamError，绝不含上游标识）；
	//   3. 本站日志保留上游真实状态码与原因，供站长排障（下游看不到这些）。
	if resp.StatusCode >= http.StatusBadRequest {
		raw, _ := readAllLimited(resp.Body, maxAdaptedBodyBytes)
		upstreamMessage := extractUpstreamErrorMessage(raw)
		identity := identityFromRequest(req.Context())
		r.recordUsage(req.Context(), usageEntry{
			UserID:        identity.UserID,
			TokenID:       identity.TokenID,
			Group:         group,
			ChannelID:     ch.ID,
			ChannelKeyID:  target.keyID,
			Model:         modelName,
			UpstreamModel: upstreamModelForLog(upstreamModel, modelRewritten),
			IsStream:      wantStream,
			// 日志记上游真实状态码：站长看到的是"上游 401"，而非被脱敏后的 502。
			StatusCode: resp.StatusCode,
			ErrorText:  truncateReason(upstreamErrorLogText(resp.StatusCode, upstreamMessage)),
		})
		writeSanitizedUpstreamError(w, adapter, resp.StatusCode)
		return forwardResponded
	}

	// 语料采集：把"返回正文副本"与 usage 抓取器合成同一个写入目标。
	// 采集器永远返回成功，因此不可能影响客户端看到的内容。
	recorder := corpus.RecorderFrom(req.Context())
	tee := responseTee(sniffer, recorder)

	if adapter == nil {
		// 直通路径：入站与上游同为 OpenAI 协议，成功响应原样透传。
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		if !flushCopy(w, resp.Body, tee) && recorder != nil {
			// 只转发了一部分（客户端断开 / 上游中断）：如实标注，
			// 免得半截对话被当成完整语料混进训练集。
			recorder.MarkAborted()
		}
	} else {
		// 转换路径：由适配器把上游的 OpenAI 响应改写为下游协议格式。
		// 注意此时响应头由 writeAdapted 决定（各协议的 Content-Type 不同）。
		r.writeAdapted(w, req, resp, adapter, sniffer, body)
	}

	// 成功响应：清零该密钥的连续失败计数（"连续失败"语义要求成功即重置）
	if resp.StatusCode < http.StatusMultipleChoices {
		r.markKeySuccess(req.Context(), target.keyID)
	}

	// 记录用量。此时响应已完整回传，写库不会影响客户端感知的延迟。
	// usage 由 sniffer 【增量】解析：无论响应多长、usage 出现在最后一个 SSE 事件里，
	// 只要上游返回过就能取到（旧实现受 256KB 上限影响，长回答会被整段丢弃而记 0）。
	usage, hasUsage := sniffer.Usage()
	identity := identityFromRequest(req.Context())
	// 用时与速率指标：在响应已完整回传后计算，不影响客户端可见延迟。
	isStream := oai.PeekStream(body)
	// 首 token 延迟（TTFB）【只对流式请求采集】。
	//
	// 为什么必须限定流式：非流式响应是一次性返回的，抓取器记录到的"首个分片"
	// 其实已经是完整响应体——把它当 TTFB 会得出"首包≈总耗时"的假数据，
	// 更糟的是速率算法里的"总耗时 − 首包"趋近于 0，会算出几十万 t/s 这种
	// 物理上不可能的数字（已实测出现 9.9 万 t/s）。非流式记 0，
	// 展示层显示「—」，语义是"未采集"而不是"首包为 0"。
	totalMS := int(time.Since(start).Milliseconds())
	firstTokenMS := 0
	if isStream {
		if first := sniffer.FirstByteAt(); !first.IsZero() {
			firstTokenMS = int(first.Sub(start).Milliseconds())
		}
	}
	entry := usageEntry{
		UserID:    identity.UserID,
		TokenID:   identity.TokenID,
		Group:     group,
		ChannelID: ch.ID,
		// 记录"本次用的是池内哪把密钥"：后台据此按密钥聚合用量，
		// 结合上游进价算出该密钥的已消耗与剩余（单密钥模式为 0）。
		ChannelKeyID: target.keyID,
		Model:        modelName,
		// 仅当映射改写了模型名时才记录上游名（否则为空串，表示与对外名一致）。
		UpstreamModel:   upstreamModelForLog(upstreamModel, modelRewritten),
		Usage:           usage,
		LatencyMS:       totalMS,
		FirstTokenMS:    firstTokenMS,
		TokensPerSecond: tokensPerSecond(usage.CompletionTokens, totalMS, firstTokenMS),
		IsStream:        isStream,
		StatusCode:      resp.StatusCode,
	}
	if !hasUsage {
		// 上游确实没返回 usage：token 只能记 0，但必须留下标注，
		// 让计费缺口在日志里可见（该列仅作提示，成功/失败仍以状态码为准）。
		entry.ErrorText = usageMissingNote
	}
	r.recordUsage(req.Context(), entry)

	return forwardResponded
}

// drainAndClose 在读空响应体后交由调用方关闭。
//
// 为什么要读完再关：直接关闭会让底层 TCP 连接无法复用，
// 高并发故障场景下会退化为"每次重试都重新建连"，进一步放大故障。
// 只读有限长度，避免恶意上游用超大响应体拖慢网关。
func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
}

// maxDrainBytes 是重试前丢弃上游响应体的最大读取量。
//
// 取值 64KiB：典型错误体只有几百字节，64KiB 足够读完以维持连接复用，
// 同时避免异常上游用超大响应体拖慢或撑爆网关。
const maxDrainBytes = 64 << 10

// isKeyLevelFailure 判断上游状态码是否属于「这把密钥自身的问题」。
//
// 判定原则："换同一渠道的另一把密钥很可能成功"才归为密钥级失败。
// 这类失败的处置优先级是「换密钥」高于「换渠道」——因为换密钥更便宜
// （同一上游、同一连接池、无需重新握手），而且能保留原本更优的渠道。
//
//   - 401：密钥无效或被吊销；
//   - 403：密钥无权访问该模型（免费额度密钥常见）；
//   - 402：余额/额度不足（部分上游的约定）；
//   - 429：该密钥被限流或额度耗尽（同一上游换一把密钥通常立即恢复）。
//
// 注意 429 同时出现在 isRetryableStatus 中：它既是密钥级也是可重试的，
// 但本函数优先把它当密钥处理（见 forwardChat 的分支顺序）。
func isKeyLevelFailure(status int) bool {
	switch status {
	case http.StatusUnauthorized, // 401
		http.StatusForbidden,       // 403
		http.StatusPaymentRequired, // 402
		http.StatusTooManyRequests: // 429
		return true
	default:
		return false
	}
}

// isRetryableStatus 判断上游状态码是否值得换渠道重试。
//
// 判定原则："换一个渠道很可能成功"才重试：
//   - 500/502/503/504：上游服务端故障；
//   - 529：上游过载（Anthropic 等厂商的约定状态码）；
//   - 429：限流/额度不足。在单密钥渠道下换渠道通常立即改善；
//     在密钥池渠道下会先尝试换密钥（见 isKeyLevelFailure）。
//
// 明确不重试的常见状态码及原因：
//   - 400/422：请求参数有误，换渠道同样失败；
//   - 401/403：在单密钥渠道下换渠道无意义（错误来自这把密钥本身）；
//   - 404：模型不存在，属于配置问题；
//   - 408/499：客户端侧问题。
func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout,      // 504
		529:                            // 上游过载（非标准码，厂商约定）
		return true
	default:
		return false
	}
}

// copyResponseHeaders 把上游响应头复制到客户端，并过滤掉不应转发的头。
//
// 过滤原因：
//   - 逐跳头（hop-by-hop）仅在单段连接内有效，代理不应转发；
//   - Content-Length / Transfer-Encoding 描述的是"上游连接"的消息边界，
//     我们回写时由 Go 的 http 包重新决定帧格式，直接复制可能造成长度不一致。
func copyResponseHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// hopByHopHeaders 是 HTTP/1.1 规范定义的逐跳头（不应被代理转发）。
var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	// 消息边界由本机重新生成，不透传上游的值
	"Content-Length": {},
}

// isHopByHopHeader 判断响应头是否应被过滤（大小写不敏感）。
func isHopByHopHeader(key string) bool {
	_, found := hopByHopHeaders[http.CanonicalHeaderKey(key)]
	return found
}

// responseTee 把 usage 抓取器与语料采集器合成一个写入目标。
//
// 为什么要合成而不是在回写循环里写两次：回写链路只留了一个 tee 挂载点，
// 用 io.MultiWriter 组合可以让两个互不相干的关注点（解析用量 / 留存原文）
// 共用同一个挂载点——新增采集就不必再动一次转发主循环，
// 也就不会因为"多写一次"而引入影响客户端响应的风险。
//
// recorder 为 nil（绝大多数请求）时直接返回 sniffer，零额外分配。
func responseTee(sniffer io.Writer, recorder io.Writer) io.Writer {
	if recorder == nil {
		return sniffer
	}
	return io.MultiWriter(sniffer, recorder)
}

// flushCopy 流式拷贝响应体，并在每个分片后立即 Flush。
//
// 参数 tee 可为 nil；非 nil 时会把内容同时写入它（用于抓取响应以解析 usage）。
// tee 的写入始终不返回错误（见 usageSniffer.Write 的实现），因此不会干扰转发。
//
// 为什么必须 Flush：大模型流式回答依赖 SSE，若数据被缓冲在网关或 HTTP 层，
// 客户端的体验会从"逐字出现"退化为"等全文生成完再一次性蹦出来"，
// 与不经网关直连相比是明显的体验倒退。
//
// 返回值表示"是否把上游读到了正常结束（EOF）"：
//
//	true  = 完整转发完毕；
//	false = 客户端中途断开或上游中断，只转发了一部分。
//	调用方据此标注语料样本的 incomplete——否则一条"半截对话"会被当成完整语料，
//	在训练集里静默地坏掉。忽略返回值的调用方行为不受影响。
func flushCopy(w http.ResponseWriter, src io.Reader, tee io.Writer) bool {
	// gin 的 ResponseWriter 实现了 http.Flusher；用类型断言兼容不支持刷新的实现
	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, copyBufferBytes)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if tee != nil {
				_, _ = tee.Write(buf[:n])
			}
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				// 客户端已断开（例如用户取消），无需继续读取上游，直接结束
				return false
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			// io.EOF 表示正常结束；其他错误（上游中断）此时已无法补救，
			// 因为响应头早已发出，只能结束本次传输。
			return readErr == io.EOF
		}
	}
}
