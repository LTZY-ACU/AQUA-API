// 本文件提供「模型测速」（逐模型测首字延迟）的转发侧实现。
//
// 意图（Why）：
//
//	渠道测活（probe.go）回答"这个渠道通不通"，只测清单里第一个能用的模型；
//	而站长更常问的是"这个渠道里【每个模型】各有多快"——同一渠道上，
//	不同模型的排队与推理速度可能相差一个数量级，只有逐模型测速才能暴露。
//
//	与测活的另两个刻意差别：
//	  1) 延迟口径用【首字延迟 TTFB】（从发出请求到收到第一个内容分片）。
//	     这是使用者真实感知的指标：流式对话里用户等的就是"第一个字"，
//	     总耗时反而会被 max_tokens 之外的偶然因素干扰。
//	  2) token 消耗压到下限：提示词只有 "ping"（约 1 token）、max_tokens=1
//	     （输出被硬性截断在 1 个 token），并且【拿到首字后立即断开连接】，
//	     不再读剩余分片——对真实上游的计费影响约为每次 2~3 token。
//	     完全 0 token 无法测量真实推理延迟（不发请求就没有延迟），这就是下限。
//
// 流转（Flow）：
//
//	server.handleSpeedTestChannel（逐模型循环）
//	  └─ Relay.ProbeChannelLatency(ctx, ch, apiKey, model)
//	       ├─ resolveUpstreamModel     渠道级映射改写（与测活同一套护栏）
//	       ├─ prepareChannelUpstream   协议转换 + 路径/鉴权组装（stream=true）
//	       ├─ r.client.Do              发出流式请求
//	       └─ 首个 data 分片到达即 Close 连接并返回 TTFB
//
// 扩展（Extend）：
//
//	需要测其他口径（如完整生成耗时、吞吐）时新增变体函数；
//	不要复制本文件的组装逻辑——路径与鉴权组装永远走 prepareChannelUpstream。
package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
)

// speedProbeMaxBodyBytes 是测速时最多读取的响应体字节数。
//
// 成功路径只需要"第一个分片"（几十到几百字节）；失败路径需要一小段错误说明。
// 上限的存在是防御性的：异常上游可能在不发任何 data 分片的情况下
// 持续输出内容，没有上限就会被它拖到超时为止。
const speedProbeMaxBodyBytes = 8192

// SpeedProbeResult 描述一次模型测速的结果。
type SpeedProbeResult struct {
	// StatusCode 是上游返回的 HTTP 状态码；0 表示请求未发出或未拿到响应。
	StatusCode int
	// TTFBMS 是首字延迟：从发出请求到收到首个内容分片的毫秒数。
	//
	// 这是测速的主指标；0 表示没有拿到任何内容（失败或超时）。
	TTFBMS int
	// TotalMS 是本次测速总耗时（从发出请求到断开连接），毫秒。
	//
	// 成功时它 ≈ TTFB + 一次往返；失败时它是等待失败的全部耗时。
	TotalMS int
	// UpstreamModel 是本次真正发给上游的模型名（可能被渠道级映射改写）。
	UpstreamModel string
	// Body 是上游响应体片段（已截断）；失败时是最有价值的排查线索。
	Body string
	// Err 是网络层错误（连接失败、超时等）；为 nil 表示拿到了响应。
	Err error
}

// TimedOut 判断这次测速是否因超时失败。
func (r SpeedProbeResult) TimedOut() bool {
	return errors.Is(r.Err, context.DeadlineExceeded)
}

// ProbeChannelLatency 用给定凭据对指定模型发起一次流式最小请求，测首字延迟。
//
// 与 ProbeChannel 的分工：测活只要"通"（非流式、读一段就走）；
// 测速要"快多少"（流式、掐表到第一个内容分片、立即断开）。
// 两者共享同一套协议组装，保证测速结论对真实调用有代表性。
func (r *Relay) ProbeChannelLatency(ctx context.Context, ch *model.Channel, apiKey, modelName string) SpeedProbeResult {
	if ch == nil {
		return SpeedProbeResult{Err: errors.New("relay: 渠道为空，无法测速")}
	}

	// 最小 token 消耗的流式请求：单字提示词 + 输出截断 1 token。
	// stream=true 让上游以 SSE 分片返回——首分片即"首字"，掐表后立即断开。
	body, err := json.Marshal(map[string]any{
		"model":      modelName,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     true,
	})
	if err != nil {
		return SpeedProbeResult{Err: fmt.Errorf("relay: 构造测速请求体失败: %w", err)}
	}

	// 与测活完全一致的两步改写（映射 → 协议组装），理由见 probe.go 同处注释：
	// 漏掉映射会让"带前缀的平台名"被原样发给上游，测出一个与真实调用无关的 404。
	upstreamModel, outboundBody, _ := r.resolveUpstreamModel(ctx, ch.ID, modelName, body)
	spec, outBody, built, err := prepareChannelUpstream(
		ch, apiKey, upstreamModel, oai.ChatCompletionsPath, outboundBody, nil, true)
	if err != nil {
		return SpeedProbeResult{UpstreamModel: upstreamModel, Err: fmt.Errorf("channeltype %s: %w", spec.Key, err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, built.URL, bytes.NewReader(outBody))
	if err != nil {
		return SpeedProbeResult{UpstreamModel: upstreamModel, Err: fmt.Errorf("relay: 构造测速请求失败: %w", err)}
	}
	req.Header = built.Header

	client := r.client
	if client == nil {
		client = http.DefaultClient
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return SpeedProbeResult{
			TotalMS:       int(time.Since(start).Milliseconds()),
			UpstreamModel: upstreamModel,
			Err:           err,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	// 非 2xx：读一小段错误说明即可返回（与测活同口径），不算测速成功。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, probeMaxBodyBytes))
		return SpeedProbeResult{
			StatusCode:    resp.StatusCode,
			TotalMS:       int(time.Since(start).Milliseconds()),
			UpstreamModel: upstreamModel,
			Body:          normalizeProbeBody(raw),
		}
	}

	// 2xx：逐行读 SSE，掐表到第一个 data 分片（首字）即断开。
	//
	// 为什么不读完整个流：读完意味着等模型把（被截断的）输出全部吐完，
	// 某些上游的收尾分片（usage / [DONE]）会晚到，白白拉长连接占用；
	// 首字延迟一旦测得，后续分片对结论毫无价值。
	reader := bufio.NewReaderSize(resp.Body, 4096)
	var readBytes int
	for {
		line, readErr := reader.ReadBytes('\n')
		readBytes += len(line)
		if len(line) > 0 {
			if payload, ok := sseDataPayload(line); ok {
				if len(payload) > 0 {
					// 首个内容分片到达：记录 TTFB 并立即断开（defer Close）。
					// TTFB 兜底至少 1ms：本机/局域网回环可能亚毫秒完成，
					// 取整为 0 会与"没有拿到首字"混淆（调用方以 TTFB>0 判定成功）。
					return SpeedProbeResult{
						StatusCode:    resp.StatusCode,
						TTFBMS:        msSinceAtLeast(start, 1),
						TotalMS:       msSinceAtLeast(start, 1),
						UpstreamModel: upstreamModel,
					}
				}
			} else if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 && !isSSEControlLine(trimmed) {
				// 防御：上游声明了流式却回了普通 JSON（首个非空行既不是 data 行
				// 也不是 SSE 控制行）。此时"首字节"同样代表响应已开始生成，
				// 按 TTFB 记录并附上片段。
				return SpeedProbeResult{
					StatusCode:    resp.StatusCode,
					TTFBMS:        msSinceAtLeast(start, 1),
					TotalMS:       msSinceAtLeast(start, 1),
					UpstreamModel: upstreamModel,
					Body:          normalizeProbeBody(line),
				}
			}
			// SSE 控制行（:注释 / event: / id: / retry:）与空行：跳过继续等首字。
		}
		if readErr != nil {
			// EOF：上游没发任何内容分片就结束了——视为拿到响应但无首字，
			// 与错误区分开（状态码是 2xx，说明渠道本身没拒绝）。
			return SpeedProbeResult{
				StatusCode:    resp.StatusCode,
				TotalMS:       int(time.Since(start).Milliseconds()),
				UpstreamModel: upstreamModel,
				Body:          "上游返回了空流（无任何内容分片）",
			}
		}
		if readBytes >= speedProbeMaxBodyBytes {
			// 异常上游：持续输出却始终没有 data 分片。放弃并明确记录原因。
			return SpeedProbeResult{
				StatusCode:    resp.StatusCode,
				TotalMS:       int(time.Since(start).Milliseconds()),
				UpstreamModel: upstreamModel,
				Body:          "上游持续输出但未出现 SSE data 分片（已按上限截断）",
			}
		}
	}
}

// isSSEControlLine 判断一行是否为 SSE 的控制行（非内容）。
//
// 这些行出现在 data 分片前后都属于正常流（注释常用作心跳），
// 若不排除，它们会被"上游没走流式"的防御分支误判成首字。
func isSSEControlLine(trimmed []byte) bool {
	return bytes.HasPrefix(trimmed, []byte(":")) ||
		bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte("id:")) ||
		bytes.HasPrefix(trimmed, []byte("retry:"))
}

// msSinceAtLeast 返回自 start 以来的毫秒数，且不低于 floor。
//
// 用途：回环/同机上游的耗时可能亚毫秒，Milliseconds() 取整得 0，
// 而"测速成功"的判定约定是 TTFB > 0——宁可把 0.4ms 记成 1ms，
// 也不能让成功的测速被误判成"没有拿到首字"。
func msSinceAtLeast(start time.Time, floor int) int {
	ms := int(time.Since(start).Milliseconds())
	if ms < floor {
		return floor
	}
	return ms
}
