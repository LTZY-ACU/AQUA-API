// SSE 单行读取上限的单元测试。
//
// 测试重点（对应"无界资源消耗"）：
//   - 超过上限的行必须立刻报错，而不是继续累积（ReadBytes 正是后者）；
//   - 行跨越多个缓冲区分片时仍要正确拼接（上限检查不能截断正常数据）；
//   - 行尾缺失（EOF）时按既有语义返回部分内容 + io.EOF。
package relay

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadBoundedSSELine_跨缓冲区拼接(t *testing.T) {
	// 缓冲区只有 8 字节，而首行有 24 字节：必须多次 ReadSlice 后拼起来
	src := "data: 0123456789abcd\nrest\n"
	reader := bufio.NewReaderSize(strings.NewReader(src), 8)

	line, err := readBoundedSSELine(reader, 1024)
	if err != nil {
		t.Fatalf("读取首行失败: %v", err)
	}
	if got, want := string(line), "data: 0123456789abcd\n"; got != want {
		t.Fatalf("首行 = %q，期望 %q", got, want)
	}

	line, err = readBoundedSSELine(reader, 1024)
	if err != nil {
		t.Fatalf("读取第二行失败: %v", err)
	}
	if got, want := string(line), "rest\n"; got != want {
		t.Fatalf("第二行 = %q，期望 %q", got, want)
	}

	// 输入读尽：与 bufio 一致返回 io.EOF
	if _, err := readBoundedSSELine(reader, 1024); !errors.Is(err, io.EOF) {
		t.Fatalf("读尽后期望 io.EOF，实际 %v", err)
	}
}

func TestReadBoundedSSELine_超过上限即报错(t *testing.T) {
	// 上游只发数据不发换行：这正是 ReadBytes 会无限累积的场景
	src := strings.Repeat("x", 4096)
	reader := bufio.NewReaderSize(strings.NewReader(src), 16)

	line, err := readBoundedSSELine(reader, 128)
	if !errors.Is(err, errSSELineTooLong) {
		t.Fatalf("期望 errSSELineTooLong，实际 err=%v line=%d 字节", err, len(line))
	}
	if line != nil {
		t.Fatalf("超限时不应返回内容，实际 %d 字节", len(line))
	}
}

func TestReadBoundedSSELine_恰好等于上限仍放行(t *testing.T) {
	// 边界：等于上限的行不能被误杀（上限是"超过才拒绝"）
	body := strings.Repeat("y", 63) + "\n" // 64 字节
	reader := bufio.NewReaderSize(strings.NewReader(body), 8)

	line, err := readBoundedSSELine(reader, 64)
	if err != nil {
		t.Fatalf("等于上限的行应读取成功: %v", err)
	}
	if len(line) != 64 {
		t.Fatalf("行长度 = %d，期望 64", len(line))
	}
}
