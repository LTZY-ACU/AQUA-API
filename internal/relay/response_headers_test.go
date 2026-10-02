// 上游响应头透传的过滤规则测试。
//
// 测试重点（都是"上游输入直达浏览器"的信任边界问题）：
//   - Set-Cookie 绝不能落到本站源上（会话固定 / Cookie 炸弹）；
//   - 浏览器可执行的正文类型（HTML/SVG）必须被改写成 octet-stream；
//   - 正常的 JSON / SSE / 图片类型原样放行，避免破坏客户端。
package relay

import (
	"net/http"
	"testing"
)

func TestCopyResponseHeaders_剥离SetCookie与可执行类型(t *testing.T) {
	src := http.Header{}
	src.Add("Content-Type", "text/html; charset=utf-8")
	src.Add("Set-Cookie", "evil=1; Path=/; Domain=.example.com")
	src.Add("Set-Cookie2", "evil2=1")
	src.Add("X-Custom", "keep-me")

	dst := http.Header{}
	copyResponseHeaders(dst, src)

	if got := dst.Get("X-Custom"); got != "keep-me" {
		t.Fatalf("普通头应原样透传，实际 %q", got)
	}
	if len(dst.Values("Set-Cookie")) != 0 || len(dst.Values("Set-Cookie2")) != 0 {
		t.Fatalf("上游 Set-Cookie 不得透传到本站源，实际 %v", dst.Values("Set-Cookie"))
	}
	if got := dst.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("HTML 应被改写为 octet-stream，实际 %q", got)
	}
}

func TestCopyResponseHeaders_放行普通类型(t *testing.T) {
	cases := map[string]string{
		"application/json":                "application/json",
		"application/json; charset=utf-8": "application/json; charset=utf-8",
		"text/event-stream":               "text/event-stream",
		"image/png":                       "image/png",
		"application/octet-stream":        "application/octet-stream",
		"text/plain; charset=utf-8":       "text/plain; charset=utf-8",
	}
	for in, want := range cases {
		dst := http.Header{}
		src := http.Header{}
		src.Set("Content-Type", in)
		copyResponseHeaders(dst, src)
		if got := dst.Get("Content-Type"); got != want {
			t.Errorf("CopyResponseHeaders(%q) Content-Type = %q，期望 %q", in, got, want)
		}
	}
}

func TestCopyResponseHeaders_逐跳头仍被过滤(t *testing.T) {
	src := http.Header{}
	src.Set("Transfer-Encoding", "chunked")
	src.Set("Connection", "keep-alive")
	src.Set("Content-Length", "123")

	dst := http.Header{}
	copyResponseHeaders(dst, src)

	for _, key := range []string{"Transfer-Encoding", "Connection", "Content-Length"} {
		if len(dst.Values(key)) != 0 {
			t.Errorf("%s 属于逐跳/消息边界头，不应被透传", key)
		}
	}
}

// TestIsBrowserExecutableType 覆盖带参数与大小写混杂的真实头值。
func TestIsBrowserExecutableType(t *testing.T) {
	executable := []string{
		"text/html",
		"TEXT/HTML; charset=utf-8",
		" application/xhtml+xml ",
		"image/svg+xml",
	}
	for _, value := range executable {
		if !isBrowserExecutableType(value) {
			t.Errorf("%q 应判定为可执行类型", value)
		}
	}

	safe := []string{
		"application/json",
		"application/json; charset=utf-8",
		"text/event-stream",
		"image/png",
		"",
	}
	for _, value := range safe {
		if isBrowserExecutableType(value) {
			t.Errorf("%q 不应判定为可执行类型", value)
		}
	}
	// 头值残缺（ParseMediaType 报错）时保持原样：宁可不改写，也不要弄坏客户端要的类型
	if isBrowserExecutableType("text/html; charset=") {
		t.Error("解析失败的头不应被判定为可执行类型")
	}
}
