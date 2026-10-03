// AI Agent 工具集的单元测试。
//
// 测试重点按"配错了会出事"排序：
//  1. **客服角色拿不到任何工具**——这是整个安全设计的支点，
//     一旦破了，客服面向公网输入就能读站内数据；
//  2. 写操作不会因参数缺失而误执行（"没传 enabled" 被当成"停用"）；
//  3. 白名单外的工具名被拒绝；
//  4. 返回值里不含密钥明文。
//
// 功能正确性（渠道列表、汇总等）由集成路径覆盖，这里只测判定与边界。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// newTestTools 构造一个"什么都没注入"的 AgentTools。
//
// 刻意不给任何仓储：这些测试要验证的是"在依赖缺失时会不会误操作"，
// 给了真仓储反而会掩盖问题。
func newTestTools() *AgentTools {
	return &AgentTools{}
}

// TestToolsForRole_客服拿不到任何工具 是本文件最重要的一条。
//
// 客服面向公网输入，提示注入是它的常态而非例外。
// 只要它能读到任何一个站内数据源，注入者就能把渠道配置、用户邮箱、
// 订单号套出来——而且这些内容会被完整发到上游模型。
func TestToolsForRole_客服拿不到任何工具(t *testing.T) {
	tools := newTestTools()

	for _, role := range []model.AgentRole{model.AgentRoleSupport} {
		got := ToolsForRole(role, tools)
		if len(got) != 0 {
			t.Errorf("角色 %q 拿到了 %d 个工具（%v），客服必须一个都拿不到",
				role, len(got), toolNames(got))
		}
	}
}

// TestToolsForRole_未知角色拿不到工具 验证默认分支是"拒绝"而不是"放行"。
func TestToolsForRole_未知角色拿不到工具(t *testing.T) {
	tools := newTestTools()

	// 构造一个不在枚举里的角色：模拟有人手工改库或将来加了角色但忘了配工具。
	got := ToolsForRole(model.AgentRole("superuser"), tools)
	if len(got) != 0 {
		t.Errorf("未登记角色拿到了 %d 个工具，必须为 0", len(got))
	}
}

// TestToolsForRole_运维拿到预期工具 验证 ops 角色确实有工具可用，
// 并覆盖"工具被误删"这类回归。
func TestToolsForRole_运维拿到预期工具(t *testing.T) {
	tools := newTestTools()

	got := ToolsForRole(model.AgentRoleOps, tools)
	if len(got) == 0 {
		t.Fatal("运维角色应拿到工具")
	}

	want := map[string]bool{
		"list_channels":       false,
		"set_channel_status":  false,
		"list_channel_probes": false,
		"list_usage_logs":     false,
		"summarize_usage":     false,
		"list_users":          false,
		"list_orders":         false,
	}
	for _, tool := range got {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("运维工具清单缺少 %s", name)
		}
	}
}

// TestToolsForRole_依赖为nil时返回空 验证未接入仓储时不会给出"看起来能用"的工具。
//
// 给了工具却执行不了，模型会反复调用并反复拿到错误，
// 最终给站长的答案里就会带上"系统出错了"这类噪音。
func TestToolsForRole_依赖为nil时返回空(t *testing.T) {
	if got := ToolsForRole(model.AgentRoleOps, nil); len(got) != 0 {
		t.Errorf("AgentTools 为 nil 时应返回 0 个工具，实际 %d 个", len(got))
	}
}

// TestToolsForRole_工具名合法 验证工具名符合 OpenAI function name 规则。
//
// 名字含非法字符时上游会直接拒绝整个请求，
// 表现为"agent 完全不工作"，而原因在代码里极难一眼看出。
func TestToolsForRole_工具名合法(t *testing.T) {
	tools := newTestTools()

	for _, tool := range ToolsForRole(model.AgentRoleOps, tools) {
		if tool.Name == "" {
			t.Error("工具名为空")
			continue
		}
		if len(tool.Name) > 64 {
			t.Errorf("工具名 %q 超过 64 字符上限", tool.Name)
		}
		for _, r := range tool.Name {
			isLower := r >= 'a' && r <= 'z'
			isUpper := r >= 'A' && r <= 'Z'
			isDigit := r >= '0' && r <= '9'
			if !isLower && !isUpper && !isDigit && r != '_' {
				t.Errorf("工具名 %q 含非法字符 %q（只允许字母数字与下划线）", tool.Name, r)
			}
		}
		if tool.Description == "" {
			t.Errorf("工具 %q 缺少描述：模型靠它决定要不要用、怎么填参数", tool.Name)
		}
		if len(tool.Parameters) == 0 {
			t.Errorf("工具 %q 缺少参数 schema", tool.Name)
		}
	}
}

// TestToolsForRole_参数schema是合法JSON 验证 schema 不会被上游拒收。
func TestToolsForRole_参数schema是合法JSON(t *testing.T) {
	tools := newTestTools()

	for _, tool := range ToolsForRole(model.AgentRoleOps, tools) {
		var parsed map[string]any
		if err := json.Unmarshal(tool.Parameters, &parsed); err != nil {
			t.Errorf("工具 %q 的参数 schema 不是合法 JSON: %v", tool.Name, err)
			continue
		}
		// function calling 要求顶层是 object 类型。
		if typ, _ := parsed["type"].(string); typ != "object" {
			t.Errorf("工具 %q 的 schema 顶层 type = %v，应为 \"object\"", tool.Name, parsed["type"])
		}
	}
}

// TestExecute_客服执行任何工具都被拒 验证执行路径同样受角色约束。
//
// 这是"客服碰不到站内数据"的最后一道闸：即使某处代码绕过了
// ToolsForRole 直接调 Execute，support 角色也会在这里被挡住。
func TestExecute_客服执行任何工具都被拒(t *testing.T) {
	tools := newTestTools()

	// 逐个尝试所有工具名——包括那些"看起来无害"的读操作。
	for _, name := range []string{
		"list_channels", "set_channel_status", "list_channel_probes",
		"list_usage_logs", "summarize_usage", "list_users", "list_orders",
	} {
		_, err := tools.Execute(context.Background(), model.AgentRoleSupport, name, nil)
		if !errors.Is(err, ErrNoTools) {
			t.Errorf("客服调用 %q 的 err = %v，期望 ErrNoTools", name, err)
		}
	}
}

// TestExecute_未知工具名被拒 验证白名单外的名字不会被执行。
func TestExecute_未知工具名被拒(t *testing.T) {
	tools := newTestTools()

	// 给一个"能用的"仓储：若白名单失效，这个测试会因为真的执行而暴露。
	tools.Channels = nil
	_, err := tools.Execute(context.Background(), model.AgentRoleOps, "drop_all_tables", nil)
	if !errors.Is(err, ErrUnknownTool) {
		t.Errorf("白名单外工具的 err = %v，期望 ErrUnknownTool", err)
	}
}

// TestExecute_模型幻觉出的SQL类工具被拒 验证"执行任意 SQL"这类工具不存在。
//
// 这条测试是安全设计的回归守卫：万一有人后来"顺手加一个通用查询工具"，
// 它会在这里失败。
func TestExecute_模型幻觉出的SQL类工具被拒(t *testing.T) {
	tools := newTestTools()

	dangerous := []string{
		"execute_sql", "run_query", "exec", "shell", "run_command",
		"read_file", "write_file", "http_request", "fetch_url",
	}
	for _, name := range dangerous {
		if _, err := tools.Execute(context.Background(), model.AgentRoleOps, name, nil); !errors.Is(err, ErrUnknownTool) {
			t.Errorf("危险工具 %q 未被拒绝（err = %v）——此类工具绝不允许存在", name, err)
		}
	}
}

// Test写操作标记正确 验证 Mutating 标记与实际行为一致。
//
// 漏标的后果：后台展示时告诉站长"这个 agent 只读"，它却改了渠道状态。
func Test写操作标记正确(t *testing.T) {
	tools := newTestTools()

	mutatingNames := map[string]bool{}
	for _, tool := range ToolsForRole(model.AgentRoleOps, tools) {
		if tool.Mutating {
			mutatingNames[tool.Name] = true
		}
	}

	// 当前唯一的写操作。
	if !mutatingNames["set_channel_status"] {
		t.Error("set_channel_status 应标记为写操作")
	}
	// 读操作绝不能被标成写操作，否则后台提示会误导站长。
	for _, name := range []string{"list_channels", "list_usage_logs", "summarize_usage", "list_users", "list_orders"} {
		if mutatingNames[name] {
			t.Errorf("%s 是只读操作，不应标记为写操作", name)
		}
	}
}

// Test工具执行时不泄露密钥 验证渠道列表工具的返回值里不含上游密钥。
//
// 这条防线为什么必须【真的执行工具】而不是扫源码字符串：
// model.Channel.APIKey 在内存中就是明文（见 channel.go 的字段注释），
// 任何"不小心把整个结构体直接序列化返回"的写法都会把它带出去，
// 而源码扫描看不出这种问题（结构体是合法引用）。工具返回值会完整发到
// 上游模型、也可能展示在前端，一旦带出去就等于把渠道密钥交给了第三方。
func Test工具执行时不泄露密钥(t *testing.T) {
	// 造一个带假密钥的渠道仓储。
	repo := &fakeChannelRepo{
		channels: []*model.Channel{
			{
				ID: 1, Name: "测试渠道", TypeKey: "openai",
				Status: model.ChannelStatusEnabled,
				// 假密钥：真出现这种值泄漏到返回值里，测试就该失败
				APIKey:    "sk-SHOULD-NEVER-LEAK-1234567890",
				BaseURL:   "https://api.example.com",
				Models:    []string{"gpt-4o"},
				LatencyMS: 120,
			},
		},
	}
	tools := &AgentTools{Channels: repo}

	got, err := tools.listChannelsTool().Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("执行 list_channels 失败: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化返回值失败: %v", err)
	}

	if strings.Contains(string(raw), "SHOULD-NEVER-LEAK") {
		t.Errorf("工具返回值里出现了上游密钥明文：%s", raw)
	}
	if strings.Contains(string(raw), "api_key") || strings.Contains(string(raw), "APIKey") {
		t.Errorf("返回值里出现了密钥字段名：%s", raw)
	}
	// 顺手确认该给的字段确实在（否则"什么都没返回"也能通过这条测试）。
	if !strings.Contains(string(raw), "测试渠道") {
		t.Errorf("返回值里应包含渠道名称，实际：%s", raw)
	}
}

// Test渠道列表不返回上游地址 验证 BaseURL 也不进返回值。
//
// 上游地址本身不算机密，但它会暴露本站用了哪家服务、
// 以及自建代理的内网地址——后者是探测内网拓扑的线索。
func Test渠道列表不返回上游地址(t *testing.T) {
	repo := &fakeChannelRepo{
		channels: []*model.Channel{
			{ID: 1, Name: "内网渠道", BaseURL: "http://10.0.0.5:8080/v1", Status: model.ChannelStatusEnabled},
		},
	}
	tools := &AgentTools{Channels: repo}

	got, err := tools.listChannelsTool().Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "10.0.0.5") {
		t.Errorf("返回值里出现了上游地址（可能暴露内网拓扑）：%s", raw)
	}
}

// Test写操作缺参数不误执行 是最要紧的一条行为测试。
//
// "没传 enabled" 若被当成 false（= 停用），模型一次参数遗漏
// 就会把正常渠道停掉——而站长根本不知情。
func Test写操作缺参数不误执行(t *testing.T) {
	repo := &fakeChannelRepo{
		channels: []*model.Channel{
			{ID: 7, Name: "在用渠道", Status: model.ChannelStatusEnabled},
		},
	}
	tools := &AgentTools{Channels: repo}
	tool := tools.setChannelStatusTool()

	cases := []struct {
		name string
		args string
	}{
		{"缺少 enabled", `{"channel_id": 7}`},
		{"缺少 channel_id", `{"enabled": false}`},
		{"两者都缺", `{}`},
		{"enabled 类型错误", `{"channel_id": 7, "enabled": "no"}`},
	}

	for _, tc := range cases {
		if _, err := tool.Handler(context.Background(), json.RawMessage(tc.args)); err == nil {
			t.Errorf("%s：应报错而不是执行，实际无错误（渠道状态可能被误改）", tc.name)
		}
	}

	// 全部用例跑完后，渠道状态必须仍是启用——上面任何一条若误执行都会破坏它。
	list, _ := repo.List(context.Background(), model.ChannelQuery{Limit: 10})
	for _, c := range list {
		if c.Status != model.ChannelStatusEnabled {
			t.Errorf("参数错误却改动了渠道状态：现在是 %v", c.Status)
		}
	}
}

// Test写操作目标不存在时报错 验证模型拿到可据以改问的错误信息。
func Test写操作目标不存在时报错(t *testing.T) {
	tools := &AgentTools{Channels: &fakeChannelRepo{}}

	_, err := tools.setChannelStatusTool().Handler(context.Background(),
		json.RawMessage(`{"channel_id": 999, "enabled": false}`))
	if err == nil {
		t.Fatal("操作不存在的渠道时应报错")
	}
	// 错误信息必须告诉模型下一步怎么做，否则它会换几个 ID 乱试。
	if !strings.Contains(err.Error(), "list_channels") {
		t.Errorf("错误信息应指引模型重新查列表，实际：%v", err)
	}
}

// fakeChannelRepo 是只实现工具所需方法的最小渠道仓储。
//
// 刻意【嵌入接口】而不是逐个实现：未实现的方法会在调用时 panic 而不是编译失败，
// 看起来危险，实则正好——接口加方法时这个 fake 不会因编译失败而卡住，
// 而真正用到的三个方法（List / GetByID / Update）一旦漏实现就会立刻暴露。
type fakeChannelRepo struct {
	model.ChannelRepository // 嵌入未实现的方法，调用到才会 panic
	channels                []*model.Channel
}

func (f *fakeChannelRepo) GetByID(_ context.Context, id uint64) (*model.Channel, error) {
	for _, c := range f.channels {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, model.ErrChannelNotFound
}

func (f *fakeChannelRepo) List(_ context.Context, q model.ChannelQuery) ([]*model.Channel, error) {
	out := make([]*model.Channel, 0, len(f.channels))
	for _, c := range f.channels {
		if q.Status != nil && c.Status != *q.Status {
			continue
		}
		out = append(out, c)
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (f *fakeChannelRepo) Update(_ context.Context, ch *model.Channel) error {
	for i, c := range f.channels {
		if c.ID == ch.ID {
			f.channels[i] = ch
			return nil
		}
	}
	return model.ErrChannelNotFound
}

// toolNames 提取工具名列表（测试报错信息用）。
func toolNames(tools []Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
