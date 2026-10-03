// 本文件钉住「上游地址与端点路径的版本段去重」行为。
//
// 意图（Why）：
//   渠道目录里大量 OpenAI 兼容类型的默认地址自带版本段（api.openai.com/v1、
//   智谱 /api/paas/v4、百炼 compatible-mode/v1），而端点常量也带 /v1 前缀，
//   直接拼接会产生 /v1/v1/chat/completions 这类重复地址（上游 404）。
//   joinUpstreamURL 负责去重，本文件保证它只在该去重的时候去重。
//
// 流转（Flow）：
//   buildUpstreamRequest / FetchModels → joinUpstreamURL(base, path)
//
// 扩展（Extend）：
//   新增非 /v1 风格版本段（如 /v2）的类型时，在此补一条用例即可。
package relay

import (
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
)

// TestJoinUpstreamURL 覆盖版本段去重的判定表：何时去重、何时保持原样。
func TestJoinUpstreamURL(t *testing.T) {
	cases := []struct {
		name, base, path, want string
	}{
		{
			name: "标准OpenAI地址_去重版本段",
			base: "https://api.openai.com/v1", path: oai.ChatCompletionsPath,
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "智谱v4版本段_同样去重v1前缀",
			base: "https://open.bigmodel.cn/api/paas/v4", path: oai.ChatCompletionsPath,
			want: "https://open.bigmodel.cn/api/paas/v4/chat/completions",
		},
		{
			name: "百炼compatible-mode_去重版本段",
			base: "https://dashscope.aliyuncs.com/compatible-mode/v1", path: oai.ChatCompletionsPath,
			want: "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		},
		{
			name: "裸域名_保留路径版本段",
			base: "https://integrate.api.nvidia.com", path: oai.ChatCompletionsPath,
			want: "https://integrate.api.nvidia.com/v1/chat/completions",
		},
		{
			name: "路径不带v1前缀_原样拼接",
			base: "https://api.anthropic.com/v1", path: "/messages",
			want: "https://api.anthropic.com/v1/messages",
		},
		{
			name: "非版本段后缀_不去重",
			base: "https://host.example.com/openai", path: oai.ChatCompletionsPath,
			want: "https://host.example.com/openai/v1/chat/completions",
		},
		{
			name: "模型列表端点_同样去重",
			base: "https://integrate.api.nvidia.com/v1", path: modelsPath,
			want: "https://integrate.api.nvidia.com/v1/models",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinUpstreamURL(tc.base, tc.path); got != tc.want {
				t.Errorf("joinUpstreamURL(%q, %q) = %q, 期望 %q", tc.base, tc.path, got, tc.want)
			}
		})
	}
}

// TestPrepareChannelUpstream_默认地址含版本段 端到端验证组装结果：
// 用 NVIDIA 类型（目录默认地址自带 /v1）拼对话端点，最终 URL 只应有一个 /v1。
func TestPrepareChannelUpstream_默认地址含版本段(t *testing.T) {
	ch := &model.Channel{
		TypeKey:  "nvidia",
		BaseURL:  "https://integrate.api.nvidia.com/v1",
		APIKey:   "nvapi-test",
		Models:   []string{"meta/llama-3.1-70b-instruct"},
		Priority: 10,
	}
	_, _, built, err := prepareChannelUpstream(
		ch, ch.APIKey, "meta/llama-3.1-70b-instruct", oai.ChatCompletionsPath, []byte(`{}`), nil, true)
	if err != nil {
		t.Fatalf("组装上游请求失败: %v", err)
	}
	want := "https://integrate.api.nvidia.com/v1/chat/completions"
	if built.URL != want {
		t.Errorf("上游 URL = %q, 期望 %q（不应出现 /v1/v1）", built.URL, want)
	}
}
