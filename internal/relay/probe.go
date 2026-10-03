// 本文件提供「上游连通性探测」（后台渠道测活）的转发侧实现。
//
// 意图（Why）：
//
//	后台的「测活」按钮需要向真实上游发一次最小请求，用来回答两个问题：
//	  1) 这个渠道地址与凭据能不能用？
//	  2) 如果不能，到底卡在哪一步？
//	此前的实现【硬编码了 OpenAI 协议】（固定拼 /v1/chat/completions、
//	固定用 Authorization: Bearer），于是 Azure / Anthropic / Gemini / Bedrock /
//	Vertex 这些渠道的测活必然失败——它们要的路径、鉴权头、请求体格式都不一样。
//	管理员看到"测活失败"会去改本来就正确的配置，属于典型的误导性结论。
//
//	因此这里刻意【复用转发链路同一套组装逻辑】（prepareChannelUpstream）：
//	测活与真实请求走完全相同的路径模板、鉴权方式与请求体转换，
//	做到"测活通过 = 真实请求大概率通过；测活失败 = 原因可信"。
//
// 流转（Flow）：
//
//	server.handleTestChannel
//	  └─ Relay.ProbeChannel(ctx, ch, apiKey, model)
//	       ├─ prepareChannelUpstream   协议转换 + 路径/鉴权组装（与转发完全一致）
//	       └─ r.client.Do              复用转发用的 HTTP 客户端（连接池、超时策略一致）
//
// 扩展（Extend）：
//
//	需要探测其他能力（嵌入/图像）时，新增一个带 path 参数的变体即可——
//	不要复制本文件的组装逻辑，路径差异应当只是入参。
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
)

// probeMaxBodyBytes 是探测时保留的上游响应体上限。
//
// 取 2KB：上游的错误说明（如"该模型未授权""额度不足"）都很短，
// 2KB 足够看清原因，又能避免被异常上游的超大响应拖住。
const probeMaxBodyBytes = 2048

// ProbeResult 描述一次上游探测的结果。
type ProbeResult struct {
	// StatusCode 是上游返回的 HTTP 状态码；0 表示请求根本没发出去/没拿到响应。
	StatusCode int
	// LatencyMS 是本次探测耗时（毫秒）。
	LatencyMS int
	// UpstreamModel 是本次真正发给上游的模型名。
	//
	// 与请求用的对外模型名可能不同（渠道级映射会改写它）。
	// 把它回传的原因是：测活报 404 时，管理员第一件要确认的事就是
	// "上游到底收到了哪个名字"——映射没生效与上游真没有该模型，
	// 处置动作完全不同。
	UpstreamModel string
	// Body 是上游响应体片段（已截断、已去掉多余空白）。
	//
	// 为什么要把上游原话透给管理员：这是排查测活失败最有效的信息——
	// "model not found" 与 "insufficient permissions" 指向完全不同的处置动作，
	// 只给一个"测活失败"会逼管理员去猜。
	Body string
	// Err 是网络层错误（连接失败、超时等）；为 nil 表示拿到了响应。
	Err error
}

// TimedOut 判断这次探测是否因超时失败。
func (r ProbeResult) TimedOut() bool {
	return errors.Is(r.Err, context.DeadlineExceeded)
}

// ProbeChannel 用给定凭据对指定模型发起一次最小请求。
//
// 参数：
//   - ch：渠道（决定协议类型、上游地址、类型专属参数与路径模板）；
//   - apiKey：本次使用的凭据明文（仅注入请求头，绝不写入日志）；
//   - modelName：用于探测的模型名（各协议都会把它放进路径或请求体）。
//
// 返回值的 StatusCode 为 0 且 Err 非 nil 表示请求未成功发出或未拿到响应；
// 其余情况都会带上看得到的上游响应（含 4xx/5xx），由调用方解释含义。
func (r *Relay) ProbeChannel(ctx context.Context, ch *model.Channel, apiKey, modelName string) ProbeResult {
	if ch == nil {
		return ProbeResult{Err: errors.New("relay: 渠道为空，无法探测")}
	}

	// 最小可用的对话请求：max_tokens=1，几乎不消耗上游额度。
	// 各协议由 prepareChannelUpstream 转换成自己的格式（Anthropic / Gemini 等）。
	body, err := json.Marshal(map[string]any{
		"model":      modelName,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	})
	if err != nil {
		return ProbeResult{Err: fmt.Errorf("relay: 构造探测请求体失败: %w", err)}
	}

	// 与转发链路完全一致的两步改写：
	//  1) 先解析渠道级模型映射，把「平台模型 ID」换成上游真正的模型名；
	//  2) 再按渠道类型组装路径与鉴权（Azure 的 deployment、Gemini 的 {model}:generateContent、
	//     api-key / x-api-key / SigV4 / Bearer 都在这里决定）。
	//
	// 第 1 步极易被忽略但后果严重：漏了它，配了映射的渠道测活会把平台名原样发给上游，
	// 得到一个与真实调用无关的 404（线上实测：带前缀的平台名被原样发出，
	// 上游回 model_not_found，而真实转发是正常的）。
	upstreamModel, outboundBody, _ := r.resolveUpstreamModel(ctx, ch.ID, modelName, body)
	spec, outBody, built, err := prepareChannelUpstream(
		ch, apiKey, upstreamModel, oai.ChatCompletionsPath, outboundBody, nil, false)
	if err != nil {
		return ProbeResult{UpstreamModel: upstreamModel, Err: fmt.Errorf("channeltype %s: %w", spec.Key, err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, built.URL, bytes.NewReader(outBody))
	if err != nil {
		return ProbeResult{UpstreamModel: upstreamModel, Err: fmt.Errorf("relay: 构造探测请求失败: %w", err)}
	}
	req.Header = built.Header

	client := r.client
	if client == nil {
		client = http.DefaultClient
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		return ProbeResult{LatencyMS: latency, UpstreamModel: upstreamModel, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// 读一小段响应体：既是排查依据，也让连接可复用。
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, probeMaxBodyBytes))
	return ProbeResult{
		StatusCode:    resp.StatusCode,
		LatencyMS:     latency,
		UpstreamModel: upstreamModel,
		Body:          normalizeProbeBody(raw),
	}
}

// normalizeProbeBody 把响应体整理成适合展示的一行文本。
//
// 处理空白与长度：上游多为紧凑 JSON（本身没有换行），但有些会返回多行文本；
// 统一压成单行并截断，避免前端表格被撑爆。
func normalizeProbeBody(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	const maxLen = 500
	if len(text) > maxLen {
		return text[:maxLen] + "…"
	}
	return text
}
