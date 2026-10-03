// 本文件实现「图像 / 音频类入站端点」的透传（非文本模态的网关入口）。
//
// 意图（Why）：
//
//	站点的模型清单里不只有文本对话：文生图（/v1/images/generations）、
//	语音合成（/v1/audio/speech）、语音转写与翻译（/v1/audio/transcriptions、
//	/v1/audio/translations）同样是使用者高频依赖的能力。这些端点与对话接口
//	共享同一套运维模型（令牌鉴权、分组路由、密钥池、失败重试、计费与调用日志），
//	差别只在"请求/响应是二进制或多模态字节流"——
//	因此它们应当是既有转发编排的一个「非文本变体」，而不是另起一套实现。
//
// 非文本变体与文本链路的三个关键差异：
//  1. 不解析、不嗅探、不改写响应体：图像是 JSON、TTS 是音频字节流、ASR 是 JSON，
//     一律逐字节回传（含 Content-Type 与状态码），保证"网关不改变任何字节"。
//  2. 请求体可能不是 JSON：ASR 用 multipart/form-data，必须保留原始
//     Content-Type（含 boundary）与字节流，禁止重新解析字段再拼装。
//  3. 错误不脱敏：文本链路会把上游错误改写成本站语义化错误码，但多模态客户端
//     （绘图插件、语音 SDK）通常需要上游的原始错误体才能给出可读提示，
//     故本文件按「原样透传上游错误」处理（回给客户端，同时照常写本站调用日志）。
//
// 流转（Flow）：
//
//	server/router.go（/v1 分组，继承 TokenAuth / RPM 限流 / 敏感词过滤）
//	  ├─ POST /v1/images/generations   → ServeImageGenerations  → serveMediaJSON
//	  ├─ POST /v1/audio/speech         → ServeAudioSpeech       → serveMediaJSON
//	  ├─ POST /v1/audio/transcriptions → ServeAudioTranscriptions → serveMediaForm
//	  └─ POST /v1/audio/translations   → ServeAudioTranslations  → serveMediaForm
//	        └─ forwardMedia（选渠道 → 选凭据 → 重试 → 计费 → 日志）
//	             └─ forwardMediaOnce（组装上游请求 → 发送 → 回写响应/透传错误）
//
// 计费：复用与转发链路同源的结算入口 r.recordUsage（成功=计费，失败=释放预留且不计费）。
//
//	按次（per_call）模型取单次价；按量模型以 token=0 计，即 0。
//
// 扩展（Extend）：
//
//	新增同类端点在媒体入口：JSON 走 serveMediaJSON，multipart 走 serveMediaForm，
//	  并在 mediaXxxPath 常量表补路径、在 router.go 的 /v1 分组注册。
//	上游若需要不同的端点形态（如 Google 系把模型名写进路径），
//	  在 forwardMediaOnce 里按渠道类型扩展路径/查询参数，切勿在处理器内硬编码。
package relay

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/oai"
)

// 媒体类上游端点路径。
//
// 与 oai 包的对话/嵌入路径同源存放于常量：路径同时被"路由注册"与
// "上游地址拼接"使用，集中定义可避免注册与转发不一致的笔误。
const (
	// mediaImagesPath 是 OpenAI 兼容的文本生图端点（同步返回 JSON）。
	mediaImagesPath = "/v1/images/generations"
	// mediaSpeechPath 是文本转语音（TTS）端点，响应为二进制音频流。
	mediaSpeechPath = "/v1/audio/speech"
	// mediaTranscriptionsPath 是语音转写（ASR）端点，入参为 multipart/form-data。
	mediaTranscriptionsPath = "/v1/audio/transcriptions"
	// mediaTranslationsPath 是语音翻译端点，入参同为 multipart/form-data。
	mediaTranslationsPath = "/v1/audio/translations"
)

// mediaFormModelMaxBytes 是从 multipart 表单里读取 model 字段的上限。
//
// 模型名只有几十字节，1KiB 足够；设上限是为了防止畸形表单用一个超大 model 字段
// 把内存撑大——我们只探测这一个字段，不该为此付出不受控的代价。
const mediaFormModelMaxBytes = 1024

// errMediaNotMultipart 表示 ASR 端点收到的 Content-Type 不是 multipart/form-data。
//
// 单独定义而非复用 oai.ErrInvalidJSON：后者会让用户看到"不是合法 JSON"这种
// 与 multipart 语义完全不符的提示，反而增加排障成本。
var errMediaNotMultipart = errors.New("relay: 音频转写/翻译请求必须是 multipart/form-data")

// ServeImageGenerations 处理 POST /v1/images/generations（文本生图透传）。
//
// 请求体是 OpenAI 兼容 JSON（model / prompt / n / size ...），响应 JSON 原样回传。
// 计费按该模型的分组价：按次模型取单次价，按量模型以 token=0 计（即 0）。
func (r *Relay) ServeImageGenerations(w http.ResponseWriter, req *http.Request) {
	r.serveMediaJSON(w, req, mediaImagesPath)
}

// ServeAudioSpeech 处理 POST /v1/audio/speech（文本转语音透传）。
//
// 请求体为 JSON；上游响应是二进制音频流（audio/mpeg 等），必须流式透传，
// 不得解析成 JSON，也不得整段缓冲——否则大音频会把内存打满、首字节也会被推迟。
// 若上游返回 4xx/5xx，则把 JSON 错误原样回传，绝不能当成音频写出去。
func (r *Relay) ServeAudioSpeech(w http.ResponseWriter, req *http.Request) {
	r.serveMediaJSON(w, req, mediaSpeechPath)
}

// ServeAudioTranscriptions 处理 POST /v1/audio/transcriptions（语音转写透传）。
//
// 入参为 multipart/form-data（含音频文件）。必须原样透传 multipart：
// 保留原始 Content-Type（含 boundary）与请求体字节流，禁止重新解析字段再拼装。
func (r *Relay) ServeAudioTranscriptions(w http.ResponseWriter, req *http.Request) {
	r.serveMediaForm(w, req, mediaTranscriptionsPath)
}

// ServeAudioTranslations 处理 POST /v1/audio/translations（语音翻译透传）。
//
// 与转写端点完全同构（同为 multipart 入参、JSON 出参），仅上游路径不同。
func (r *Relay) ServeAudioTranslations(w http.ResponseWriter, req *http.Request) {
	r.serveMediaForm(w, req, mediaTranslationsPath)
}

// serveMediaJSON 是"JSON 入参"媒体端点的公共前置：读体 → 取 model → 转发。
//
// 复用 oai.ReadBody（限长 + 还原 body）与 oai.PeekModel（只探测 model 字段），
// 与对话链路保持同一份请求体读取语义，避免"鉴权读过 body、转发读到空"的经典坑。
func (r *Relay) serveMediaJSON(w http.ResponseWriter, req *http.Request, upstreamPath string) {
	body, err := oai.ReadBody(req)
	if err != nil {
		writeMediaBodyError(w, err)
		return
	}

	modelName, err := oai.PeekModel(body)
	if err != nil {
		writeMediaBodyError(w, err)
		return
	}

	r.forwardMedia(w, req, modelName, body, upstreamPath, req.Header.Get("Content-Type"))
}

// serveMediaForm 是"multipart 入参"媒体端点的公共前置：读体 → 取表单里的 model → 转发。
//
// 关键：body 保持逐字节原样（ReadBody 已还原），转发时连同原始 Content-Type
// （含 boundary）一起送给上游；这里只对 body 的一份只读副本解析 model 字段。
func (r *Relay) serveMediaForm(w http.ResponseWriter, req *http.Request, upstreamPath string) {
	body, err := oai.ReadBody(req)
	if err != nil {
		writeMediaBodyError(w, err)
		return
	}

	contentType := req.Header.Get("Content-Type")
	modelName, err := peekFormModel(contentType, body)
	if err != nil {
		writeMediaBodyError(w, err)
		return
	}

	r.forwardMedia(w, req, modelName, body, upstreamPath, contentType)
}

// peekFormModel 从 multipart 表单里取出 model 字段的值。
//
// 实现要点：
//   - 只在内存里解析 body 的副本（bytes.NewReader），不消费原始字节流，
//     因此转发阶段仍能拿到完整 body；
//   - 逐个 part 扫描、命中 model 即返回；NextPart 会自动丢弃未读完的上一个 part，
//     不会因大文件被整体读入内存。
//
// 返回 oai.ErrMissingModel 表示表单里没有 model；返回 errMediaNotMultipart 表示
// Content-Type 不是 multipart（含缺少 boundary 的情况）。
func peekFormModel(contentType string, body []byte) (string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		return "", errMediaNotMultipart
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return "", errMediaNotMultipart
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errMediaNotMultipart
		}
		if part.FormName() != "model" {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(part, mediaFormModelMaxBytes))
		if err != nil {
			return "", err
		}
		model := strings.TrimSpace(string(raw))
		if model == "" {
			return "", oai.ErrMissingModel
		}
		return model, nil
	}
	return "", oai.ErrMissingModel
}

// writeMediaBodyError 把"读取/解析媒体请求体"的错误映射为 HTTP 响应。
//
// 与文本链路的错误码保持一致的语义（超限 413、缺 model 400、其余 400），
// 便于使用者在两类端点间迁移时看到同一种提示。
func writeMediaBodyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, oai.ErrRequestTooLarge):
		oai.WriteError(w, http.StatusRequestEntityTooLarge,
			"请求体超过上限", oai.TypeInvalidRequest, oai.CodeRequestTooLarge)
	case errors.Is(err, oai.ErrMissingModel):
		oai.WriteError(w, http.StatusBadRequest,
			"缺少 model 字段", oai.TypeInvalidRequest, oai.CodeMissingModel)
	default:
		oai.WriteError(w, http.StatusBadRequest,
			"请求体格式不正确（应为 JSON，音频转写/翻译应为 multipart/form-data）",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
	}
}

// mediaFailure 记录"最后一次上游失败响应"，用于所有重试耗尽后的原样透传。
//
// 为什么要留存完整响应（状态码 + 响应头 + 响应体）而不是只留状态码：
// 本文件对多模态错误采取"原样透传"，重试耗尽时必须能把上游最后一次的真实响应
// 回给客户端；只留状态码会让客户端拿到一个没有正文的空错误。
type mediaFailure struct {
	status       int
	header       http.Header
	body         []byte
	channelID    uint64
	channelKeyID uint64
}

// forwardMedia 按既有路由策略选择渠道与凭据并转发媒体请求，失败时按类型重试。
//
// 与文本链路 forwardWithFallback 同构（候选集 → 选渠道 → 选凭据 → 重试预算 →
// 计费/日志），差异仅在"响应与错误的回写方式"（原样透传，见文件头说明）。
// 因此这里刻意复用同一批函数：listCandidates / pickCandidate / resolveChatCredential /
// hasOtherChannel / recordUsage，保证两条链路的路由与计费语义不会漂移。
func (r *Relay) forwardMedia(w http.ResponseWriter, req *http.Request,
	modelName string, body []byte, upstreamPath, inboundContentType string) {

	// 分组只解析一次：选渠道、重试换渠道、计费必须用同一个值，避免串组错账。
	group := r.groupFromContext(req.Context())

	candidates, err := r.listCandidates(req.Context(), group, modelName)
	if err != nil {
		oai.WriteError(w, http.StatusInternalServerError, "网关内部错误",
			oai.TypeServer, oai.CodeInternal)
		return
	}
	if len(candidates) == 0 {
		// 无可用渠道：与文本链路保持一致的语义化提示（区分"该分组未开通"与"全站无渠道"）。
		anyChannel := r.channelCountForModel(req.Context(), modelName)
		message := "当前没有可用的上游渠道能处理该模型"
		if anyChannel > 0 {
			message = "当前分组未开通该模型（模型在其他分组可用），请检查令牌分组或联系管理员"
		}
		identity := identityFromRequest(req.Context())
		r.recordUsage(req.Context(), usageEntry{
			UserID:     identity.UserID,
			TokenID:    identity.TokenID,
			Group:      group,
			Model:      modelName,
			StatusCode: http.StatusServiceUnavailable,
			ErrorText:  "无可用渠道（分组=" + group + "）",
		})
		oai.WriteError(w, http.StatusServiceUnavailable, message,
			oai.TypeServer, oai.CodeNoAvailableChannel)
		return
	}

	excludedChannels := make(map[uint64]struct{})
	usedKeys := make(map[uint64]struct{})
	// 会话粘性：与文本链路一致，客户端带约定请求头时整轮重试粘在同一凭据上。
	keyCtx := withCredentialSession(req.Context(), stickySessionKey(req))

	// 重试预算由首选渠道代表本次请求（与文本链路同口径），行为稳定可预期。
	retryPolicy := candidates[0].RetryPolicyFor(modelName)
	channelBudget := retryPolicy.MaxAttempts
	if !retryPolicy.Enabled {
		channelBudget = 1
	}
	channelAttempts := 0

	// 最后一次上游失败响应：所有重试用尽后原样回传给客户端（status==0 表示无）。
	var last mediaFailure

retryLoop:
	for keyAttempts := 1; keyAttempts <= maxKeyLevelAttempts; keyAttempts++ {
		ch := pickCandidate(candidates, excludedChannels)
		if ch == nil {
			break
		}

		cred, ok, hasSpareKey := r.resolveChatCredential(keyCtx, ch, usedKeys,
			credentialScope{Group: group, Model: modelName})
		if !ok {
			// 该渠道当前没有可用凭据：跳过并换渠道，不浪费尝试预算。
			r.logChannelSkipped(keyCtx, ch, modelName)
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
			attempt:         keyAttempts,
		}

		// 与 openai.go 同一口径：只有确实占上才释放，
		// 否则 acquireKey 失败时也会减一次 in_flight，计数会被减成负数。
		acquired := r.acquireKey(keyCtx, target.keyID)
		outcome := r.forwardMediaOnce(w, req, target, group, modelName,
			body, upstreamPath, inboundContentType, &last)
		r.releaseKey(target.keyID, acquired)

		switch outcome {
		case forwardResponded:
			return
		case forwardRetryKey:
			continue
		case forwardRetryChannel:
			excludedChannels[ch.ID] = struct{}{}
			channelAttempts++
			if channelAttempts >= channelBudget {
				break retryLoop
			}
			continue
		}
	}

	// 所有尝试用尽：若有留存的上游失败响应则原样透传，否则回笼统的 502。
	if last.status != 0 {
		identity := identityFromRequest(req.Context())
		r.recordUsage(req.Context(), usageEntry{
			UserID:       identity.UserID,
			TokenID:      identity.TokenID,
			Group:        group,
			ChannelID:    last.channelID,
			ChannelKeyID: last.channelKeyID,
			Model:        modelName,
			StatusCode:   last.status,
			ErrorText:    truncateReason(upstreamErrorLogText(last.status, extractUpstreamErrorMessage(last.body))),
		})
		copyResponseHeaders(w.Header(), last.header)
		w.WriteHeader(last.status)
		_, _ = w.Write(last.body)
		return
	}

	identity := identityFromRequest(req.Context())
	r.recordUsage(req.Context(), usageEntry{
		UserID:     identity.UserID,
		TokenID:    identity.TokenID,
		Group:      group,
		Model:      modelName,
		StatusCode: http.StatusBadGateway,
		ErrorText:  "所有候选渠道均请求失败",
	})
	oai.WriteError(w, http.StatusBadGateway, "所有候选渠道均请求失败",
		oai.TypeServer, oai.CodeUpstreamRequestFailed)
}

// forwardMediaOnce 把一次媒体请求转发到指定目标（渠道 + 凭据）并回写响应。
//
// 返回值语义与 forwardChat 一致（forwardOutcome），供上层决定重试方向。
//
// 回写规则：
//   - 成功（< 400）：原样透传状态码、响应头（含 Content-Type）与响应体（流式）；
//   - 失败（>= 400）：
//   - 若属"换把密钥很可能成功"（401/403/402/429）且同渠道还有备用密钥 → 换密钥重试；
//   - 否则若属可重试的渠道故障（5xx/429）且还有别的渠道 → 换渠道重试；
//   - 否则即终态：原样透传上游错误（状态码 + 响应头 + 响应体）。
//
// 计费：仅成功响应产生计费；失败响应走 recordUsage 的错误分支（释放预留、不计费）。
func (r *Relay) forwardMediaOnce(w http.ResponseWriter, req *http.Request,
	target forwardTarget, group, modelName string, body []byte,
	upstreamPath, inboundContentType string, last *mediaFailure) forwardOutcome {

	start := time.Now()
	ch := target.channel
	identity := identityFromRequest(req.Context())

	// 渠道级模型映射：JSON 请求体自动改写 model；multipart 无法安全改写则原样透传
	//（rewriteRequestModel 对非 JSON 会返回 ok=false，行为天然正确）。
	upstreamModel, outboundBody, modelRewritten := r.resolveUpstreamModel(
		req.Context(), ch.ID, modelName, body)

	// 请求头：透传 Accept / User-Agent，并显式带上原始 Content-Type。
	// 对 multipart 而言 Content-Type 含 boundary，必须逐字节保留——丢了 boundary
	// 上游将无法解析表单；对 JSON 而言则保持 application/json。
	headers := upstreamForwardHeaders(req.Header)
	if ct := strings.TrimSpace(inboundContentType); ct != "" {
		headers.Set("Content-Type", ct)
	}

	_, outBody, built, prepareErr := prepareChannelUpstreamFor(
		ch, target.apiKey, upstreamModel, upstreamPath, outboundBody,
		headers, false, target.keyMeta)
	if prepareErr != nil {
		if errors.Is(prepareErr, errRequestBodyConversion) {
			// 请求体无法转换为该上游协议（如把 multipart 交给 Anthropic 类型渠道）：
			// 属调用方请求问题，换渠道也不会成功，直接回 400。
			oai.WriteError(w, http.StatusBadRequest, prepareErr.Error(),
				oai.TypeInvalidRequest, "request_conversion_failed")
			return forwardResponded
		}
		// 组装失败（如渠道未配地址）：尚未写出任何响应，可换渠道重试。
		return forwardRetryChannel
	}

	// 用请求 context：客户端断开时自动取消上游请求，避免无谓的上游消耗。
	upReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, built.URL, bytes.NewReader(outBody))
	if err != nil {
		return forwardRetryChannel
	}
	upReq.Header = built.Header

	resp, err := r.client.Do(upReq)
	if err != nil {
		// 连接失败与密钥无关，可安全换渠道重试。
		return forwardRetryChannel
	}
	defer func() { _ = resp.Body.Close() }()

	// 折扣分组重试率统计：每次真实打到上游都计一次（含重试）。
	if r.billing != nil {
		r.billing.recordUpstreamCall(group, false)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		// 读走上游错误体：既要原样回传，也要写进本站日志（供站长排障）。
		raw, _ := readAllLimited(resp.Body, maxAdaptedBodyBytes)

		// 凭据级失败（401/403/402/429）且同渠道还有备用密钥 → 换密钥重试。
		if isKeyLevelFailure(resp.StatusCode) && target.hasSpareKey {
			r.applyCredentialFailure(req.Context(), ch, target.keyID, target.keyFailCount, resp.StatusCode, raw)
			recordMediaFailure(last, resp, raw, ch.ID, target.keyID)
			return forwardRetryKey
		}
		// 渠道级可重试故障（5xx/429）且还有别的渠道 → 换渠道重试。
		if isRetryableStatus(resp.StatusCode) && target.hasSpareChannel {
			r.applyCredentialFailure(req.Context(), ch, target.keyID, target.keyFailCount, resp.StatusCode, nil)
			recordMediaFailure(last, resp, raw, ch.ID, target.keyID)
			return forwardRetryChannel
		}

		// 终态：记录调用日志（失败不计费）并原样透传上游错误。
		r.recordUsage(req.Context(), usageEntry{
			UserID:        identity.UserID,
			TokenID:       identity.TokenID,
			Group:         group,
			ChannelID:     ch.ID,
			ChannelKeyID:  target.keyID,
			Model:         modelName,
			UpstreamModel: upstreamModelForLog(upstreamModel, modelRewritten),
			LatencyMS:     int(time.Since(start).Milliseconds()),
			StatusCode:    resp.StatusCode,
			ErrorText:     truncateReason(upstreamErrorLogText(resp.StatusCode, extractUpstreamErrorMessage(raw))),
		})
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return forwardResponded
	}

	// ── 成功响应：原样透传状态码、响应头与响应体（流式）────────────────
	// Content-Type 由 copyResponseHeaders 一并带回（audio/mpeg、application/json 等），
	// 客户端据此正确解码；flushCopy 逐块 Flush，保证音频/大 JSON 首字节尽快到达。
	copyResponseHeaders(w.Header(), resp.Header)
	routingHeaders{
		channelID:   ch.ID,
		channelName: ch.Name,
		attempts:    target.attempt,
		upstream:    upstreamModel,
	}.apply(w.Header())
	w.WriteHeader(resp.StatusCode)
	flushCopy(w, resp.Body, nil)

	r.markKeySuccess(req.Context(), target.keyID)

	// 计费与日志：与文本链路同源（recordUsage → settleQuota）。
	// 媒体请求无 token 用量，故 Usage 保持零值：按次模型据此取单次价，按量模型计 0。
	r.recordUsage(req.Context(), usageEntry{
		UserID:        identity.UserID,
		TokenID:       identity.TokenID,
		Group:         group,
		ChannelID:     ch.ID,
		ChannelKeyID:  target.keyID,
		Model:         modelName,
		UpstreamModel: upstreamModelForLog(upstreamModel, modelRewritten),
		LatencyMS:     int(time.Since(start).Milliseconds()),
		StatusCode:    resp.StatusCode,
	})
	return forwardResponded
}

// recordMediaFailure 把一次上游失败响应留存到 last（供重试耗尽后原样透传）。
//
// 复制响应头而非直接引用 resp.Header：resp.Body 关闭后头仍有效，但复制能避免
// 后续对同一 map 的意外修改影响已留存的内容。
func recordMediaFailure(last *mediaFailure, resp *http.Response, raw []byte, channelID, keyID uint64) {
	if last == nil || resp == nil {
		return
	}
	*last = mediaFailure{
		status:       resp.StatusCode,
		header:       resp.Header.Clone(),
		body:         raw,
		channelID:    channelID,
		channelKeyID: keyID,
	}
}
