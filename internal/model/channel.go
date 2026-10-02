// Package model 定义领域模型与仓储接口（不包含任何 SQL 与 HTTP 细节）。
//
// 意图（Why）：
//
//	把「业务概念」与「技术实现」解耦：本包只描述渠道、令牌等实体长什么样、
//	有哪些行为与约束；具体存到哪、怎么存，由 internal/store 实现本包定义的接口。
//	这样做的收益：
//	  1) 上层（server / relay）只依赖接口，替换存储实现无需改动业务代码；
//	  2) 领域规则（校验、状态判断）集中一处，避免散落在各处造成不一致。
//
// 流转（Flow）：
//
//	internal/store 实现 ChannelRepository 接口
//	  └─ cmd/aqua/main.go 装配后注入 internal/server 与 internal/relay
//	       └─ 业务代码仅面对 model.Channel 与 model.ChannelRepository
//
// 扩展（Extend）：
//
//	新增实体（如 Token、User）：
//	  1) 在本包新增实体文件（如 token.go），定义结构体 + 校验 + 仓储接口；
//	  2) 在 internal/store 下新增对应实现；
//	  3) 若需要新表，同步在 store/schema.sql 追加迁移。
//	注意：本包不得导入 internal/store、internal/server 等上层包，避免循环依赖。
package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitee.com/xiaosu4610/aqua-api/internal/channeltype"
)

// 领域错误定义。
//
// 设计说明：业务层通过 errors.Is 判断错误类型，而不是比较错误字符串，
// 因此这些错误应由仓储实现原样返回（可用 %w 包装）。
var (
	// ErrChannelNotFound 表示按条件未找到渠道。
	ErrChannelNotFound = errors.New("model: 渠道不存在")
)

// ChannelStatus 表示渠道的可用状态。
//
// 取值与数据库字段 channels.status 一一对应，禁止随意改动数值
// （已落库的数据依赖这些值）。
type ChannelStatus int

const (
	// ChannelStatusEnabled 启用：可参与路由选择。
	ChannelStatusEnabled ChannelStatus = 1
	// ChannelStatusDisabled 手动禁用：由管理员主动关闭，自动健康检查不会将其恢复。
	ChannelStatusDisabled ChannelStatus = 2
	// ChannelStatusAutoDisabled 自动禁用：由熔断/健康检查判定不可用而关闭，
	// 后续健康检查通过后可由系统自动恢复。
	ChannelStatusAutoDisabled ChannelStatus = 3
)

// String 返回状态的中文名，便于日志与前端展示。
func (s ChannelStatus) String() string {
	switch s {
	case ChannelStatusEnabled:
		return "启用"
	case ChannelStatusDisabled:
		return "手动禁用"
	case ChannelStatusAutoDisabled:
		return "自动禁用"
	default:
		return fmt.Sprintf("未知(%d)", int(s))
	}
}

// IsValid 判断状态值是否合法（用于校验外部输入）。
func (s ChannelStatus) IsValid() bool {
	switch s {
	case ChannelStatusEnabled, ChannelStatusDisabled, ChannelStatusAutoDisabled:
		return true
	default:
		return false
	}
}

// Channel 表示一个上游渠道（一个供应商接入配置）。
//
// 重要安全约定：
//
//	APIKey 字段在内存中是【明文】，仅在进程内流转；
//	落库时由仓储负责加密（见 internal/store 与 internal/crypto），
//	读取时由仓储解密。因此本结构体绝不可被直接序列化返回给前端——
//	对外输出请使用 MaskedAPIKey()。
type Channel struct {
	ID   uint64 // 主键，新建时为 0（由数据库生成）
	Name string // 显示名，如 "OpenAI 官方"
	Type int    // 渠道类型编号（兼容字段；M1 起统一为 1）
	// TypeKey 是渠道类型标识（与 channeltype 目录的 Key 对应，如 azure_openai）。
	//
	// 空串表示"未指定类型"：转发层据此回退为 OpenAI 兼容，保证历史渠道行为不变。
	// 非空时必须是已登记的类型，否则协议适配器无从选择（见 Validate）。
	TypeKey string
	// ExtraConfig 是类型专属扩展参数（如 Azure 的 deployment / api_version）。
	//
	// 语义为"键值字符串对"，与 channeltype.Type.ExtraFields 的 Key 对应；
	// 落库时序列化为 JSON 对象。协议适配器据此补齐路径占位符与查询参数。
	ExtraConfig map[string]string
	BaseURL     string   // 上游基础地址，如 https://api.openai.com
	APIKey      string   // 上游密钥【明文，仅内存】
	Models      []string // 可用模型列表
	Group       string   // 主分组（= Groups 的第一项），用于展示与分组统计
	// Groups 是本渠道可服务的全部分组（落库于 channels.group_names）。
	//
	// 为什么需要多分组：同一上游常常要同时服务免费用户与付费用户，
	// 或在新分组上线初期先让老渠道覆盖过去。若一个渠道只能属于一个分组，
	// 用户拿着别的分组的令牌调用时会因"没有候选渠道"直接 503（线上实测过）。
	//
	// 约定：为空表示"未显式配置"，此时按 [Group] 单分组处理（既有行为不变）。
	// 写入路径由仓储保证 Group 恒等于 Groups[0]，不会出现两者互相矛盾。
	Groups   []string
	Priority int           // 优先级，数值越大越优先
	Weight   int           // 同优先级内的随机权重，需 > 0
	Status   ChannelStatus // 可用状态
	// KeyStrategy 是本渠道凭据池的调度策略（落库于 channels.key_strategy）。
	//
	// 空值在落库时由仓储归一为 DefaultKeyStrategy()（最少在途），
	// 因此读取回来的值恒为合法策略；取值与语义见 KeyStrategy 常量。
	KeyStrategy KeyStrategy
	// KeyFailurePolicy 是本渠道凭据失败后的处置策略（落库于 channels.key_failure_policy）。
	//
	// 空值在落库时由仓储归一为 DefaultKeyFailurePolicy()（只冷却不摘除）。
	// 取值与语义见 KeyFailurePolicy 常量——它决定的是一件很有运营后果的事：
	// "一把密钥失败后是回到池子，还是永久退出"。
	KeyFailurePolicy KeyFailurePolicy
	// KeyCooldownSeconds 是本渠道凭据失败后的统一冷却时长（秒）。
	//
	// 0 表示使用内置的分级指数退避（见 relay 的 backoff）；
	// >0 表示所有凭据级失败统一冷却该秒数——站长想表达"进冷却池多久"时用这个。
	// 上限见 MaxKeyCooldownSeconds：过长的冷却等价于"事实摘除"，
	// 会让站长以为自己没设摘除策略却观察到密钥不回来。
	KeyCooldownSeconds int
	// RetryMode 是本渠道的上游错误重试总开关（落库 channels.retry_enabled）。
	//
	// 零值 RetryModeUnset 表示"未配置"，按开启处理——与迁移前的硬编码行为一致，
	// 因此旧数据与未显式赋值的渠道都不会因缺省而改变行为（详见 channel_retry.go）。
	RetryMode RetryMode
	// RetryMaxAttempts 是本渠道的渠道级重试次数上限；0 表示使用默认值（3 次）。
	//
	// 注意它约束的是"换渠道/换密钥"的总预算，不是单把密钥的尝试次数——
	// 具体语义见 relay.forwardWithFallback 的注释。
	RetryMaxAttempts int
	// ModelRetryRules 是模型级重试覆盖规则（对外模型名，支持尾部通配符 *）。
	//
	// 为空表示所有模型都沿用渠道级配置。解析次序见 RetryPolicyFor。
	ModelRetryRules []ModelRetryRule
	CreatedAt       time.Time // 创建时间
	UpdatedAt       time.Time // 更新时间

	// LastTestAt 是最近一次测活时间；零值表示从未测活。
	LastTestAt time.Time
	// LastTestOK 表示最近一次测活是否通过。
	//
	// 说明：这是"最近一次"的瞬时结果，不是健康状态本身——
	// 渠道是否可用要看 Status（自动禁用由健康检查写入）。
	LastTestOK bool
	// LatencyMS 是最近一次测活的总耗时（毫秒）；0 表示从未拿到过响应。
	//
	// 留意它是一次测量而非平均值：单点到点抖动很大，因此展示层会同时给出
	// 测量时间 LastTestAt 让管理员判断这个数字是否还新鲜。
	LatencyMS int
	// LastTestCode 是最近一次测活的上游 HTTP 状态码；0 表示请求没发出去或没拿到响应。
	//
	// 保留状态码而不是只留"成功与否"：401（密钥失效）、404（模型下架）、
	// 429（上游限流）指向完全不同的处置动作，布尔量会让这三条线索一起丢掉。
	LastTestCode int
	// LastTestModel 是最近一次实际发到上游的模型名。
	//
	// 渠道配置多个模型时，测活会按清单顺序探测到第一个可用为止，
	// 记录命中者才能解释"渠道到底还能用哪个模型"。
	LastTestModel string
}

// ChannelProbeResult 是一次渠道测活要落库的完整结论。
//
// 为什么把它做成结构体而不是一串参数：调用方日后新增一个字段（例如上游返回的
// 速率余量）时，一串 bool/int 参数会把"哪一个参数是状态码"变成猜谜，
// 而结构体的字段名本身就是文档。
type ChannelProbeResult struct {
	ID         uint64    // 渠道主键
	At         time.Time // 本次测活时刻
	OK         bool      // 是否可用（至少有一个模型返回 2xx）
	LatencyMS  int       // 本次测活耗时（毫秒）
	StatusCode int       // 上游状态码；0 = 网络层失败
	Model      string    // 实际探测命中的模型名（可能已被渠道映射改写）
}

// GroupList 返回渠道可服务的分组清单（去重、去空白）。
//
// 语义：Groups 非空时以它为准；为空则回退为 [Group]（既有单分组数据）。
// 返回值恒非空——Group 也为空时给一个默认分组名，避免调用方拿到空切片后
// 在界面上显示成"不属于任何分组"这种不可用状态。
func (c *Channel) GroupList() []string {
	if c == nil {
		return nil
	}
	if result := NormalizeGroupNames(c.Groups); len(result) > 0 {
		return result
	}
	if name := strings.TrimSpace(c.Group); name != "" {
		return []string{name}
	}
	return []string{DefaultGroupName}
}

// NormalizeGroupNames 归一化分组清单：去空白、去空项、去重，并保持输入顺序。
//
// 为什么顺序重要：约定"主分组 = 清单第一项"，顺序决定界面与统计里显示哪个名字，
// 因此不能为了去重而排序。
func NormalizeGroupNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, duplicated := seen[name]; duplicated {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}

// MatchesGroup 判断该渠道是否服务于指定分组。
//
// group 为空时视为"不按分组过滤"（与 ChannelQuery.Group 为空不过滤的语义一致），
// 直接返回 true，避免调用方各自处理空值导致行为漂移。
func (c *Channel) MatchesGroup(group string) bool {
	group = strings.TrimSpace(group)
	if group == "" {
		return true
	}
	for _, name := range c.GroupList() {
		if name == group {
			return true
		}
	}
	return false
}

// Validate 校验渠道的必要字段，供创建与更新时调用。
//
// 设计原则：把校验放在领域层而非 HTTP 层，保证无论从哪条路径写入
// （API、CLI、批量导入）都遵守同一套规则。
func (c *Channel) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("渠道名称不能为空")
	}
	if c.Type <= 0 {
		return fmt.Errorf("渠道类型非法: %d（必须为正整数）", c.Type)
	}
	// 类型标识非空时才校验：空串是历史渠道的合法状态（回退为 OpenAI 兼容）。
	// 非空则必须是已登记的类型——否则转发层选不出协议适配器与鉴权方式，
	// 会以 400/401 的形式在上游暴露，且报错含义模糊难以定位。
	if key := strings.TrimSpace(c.TypeKey); key != "" {
		if _, ok := channeltype.Find(key); !ok {
			return fmt.Errorf("渠道类型标识非法: %q（未在渠道类型目录中登记，可用：%s）",
				key, strings.Join(channeltype.Keys(), " / "))
		}
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("渠道 base_url 不能为空")
	}
	// 只做基础格式检查，不引入 net/url 解析：允许使用者填写内网地址等特殊形式
	if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		return fmt.Errorf("渠道 base_url 必须以 http:// 或 https:// 开头，当前为 %q", c.BaseURL)
	}
	if c.Weight <= 0 {
		return fmt.Errorf("渠道权重必须大于 0，当前为 %d（权重为 0 会导致该渠道永远不会被选中）", c.Weight)
	}
	if c.Priority < 0 {
		return fmt.Errorf("渠道优先级不能为负数，当前为 %d", c.Priority)
	}
	if !c.Status.IsValid() {
		return fmt.Errorf("渠道状态非法: %d", int(c.Status))
	}
	if strings.TrimSpace(c.Group) == "" {
		return errors.New("渠道分组不能为空")
	}
	// 策略为空表示"未指定"，由仓储归一为默认策略；只拦明显非法的取值。
	if c.KeyStrategy != "" && !c.KeyStrategy.IsValid() {
		return fmt.Errorf("渠道凭据调度策略非法: %q（可选：%s）",
			string(c.KeyStrategy), KeyStrategyOptionText())
	}
	// 同理：失败策略为空 = 未指定，由仓储归一为默认（只冷却不摘除）。
	if c.KeyFailurePolicy != "" && !c.KeyFailurePolicy.IsValid() {
		return fmt.Errorf("密钥失败策略非法: %q（可选：%s）",
			string(c.KeyFailurePolicy), KeyFailurePolicyOptionText())
	}
	if c.KeyCooldownSeconds < 0 || c.KeyCooldownSeconds > MaxKeyCooldownSeconds {
		return fmt.Errorf("密钥冷却时长必须在 0 ~ %d 秒之间（当前 %d；0 表示使用系统默认的分级退避）",
			MaxKeyCooldownSeconds, c.KeyCooldownSeconds)
	}
	// 重试策略（迁移 0037）：开关取值、次数区间与模型级规则
	if err := c.validateRetryPolicy(); err != nil {
		return err
	}
	return nil
}

// HasModel 判断本渠道是否声明支持指定模型。
//
// 匹配规则为精确匹配（大小写敏感）。M1 暂不支持通配符与模型映射，
// 这些能力会在 M2 引入（届时模型映射会在路由前处理）。
func (c *Channel) HasModel(name string) bool {
	for _, m := range c.Models {
		if m == name {
			return true
		}
	}
	return false
}

// MaskedAPIKey 返回脱敏后的密钥，供日志与接口输出使用。
//
// 脱敏规则：保留前 6 位与后 4 位，中间用 **** 替代。
// 例如 "nvapi-abcdefghijklmn" → "nvapi-****klmn"。
// 说明：截断展示既能帮助运维辨认是"哪把钥匙"，又不足以被直接盗用。
//
// 长度下限（minMaskableLen）：保留 6+4=10 个字符，因此只有当密钥明显长于它时
// 才值得露片段。否则 11 位的密钥会露出 10 位（几乎等于明文）——
// 短密钥本身强度就弱，脱敏的意义恰恰在这里最大，所以宁可不露。
func (c *Channel) MaskedAPIKey() string {
	const (
		keepPrefix = 6
		keepSuffix = 4
		// minMaskableLen 是"允许露出前后缀"的最小密钥长度（20 位时恰好露一半）
		minMaskableLen = 20
	)
	key := c.APIKey
	if len(key) < minMaskableLen {
		return strings.Repeat("*", len(key))
	}
	return key[:keepPrefix] + "****" + key[len(key)-keepSuffix:]
}

// ChannelQuery 描述渠道列表的查询条件。
//
// 使用指针表示"可选过滤"：Status 为 nil 时表示不按状态过滤。
type ChannelQuery struct {
	Group  string         // 按分组过滤；空字符串表示不过滤
	Status *ChannelStatus // 按状态过滤；nil 表示不过滤
	Limit  int            // 返回条数上限；<=0 时使用默认值
	Offset int            // 偏移量，用于分页
}

// ChannelRepository 定义渠道的持久化操作。
//
// 约定：
//   - 所有方法的实现都必须保证 APIKey 在落库时被加密、读取时被解密；
//   - 未找到记录时，GetByID 返回 ErrChannelNotFound（调用方用 errors.Is 判断）。
type ChannelRepository interface {
	// Create 新增渠道，成功后回填 ID、CreatedAt、UpdatedAt。
	Create(ctx context.Context, ch *Channel) error

	// GetByID 按主键查询渠道，不存在时返回 ErrChannelNotFound。
	GetByID(ctx context.Context, id uint64) (*Channel, error)

	// List 按条件查询渠道列表，固定按「优先级降序、权重降序、ID 升序」返回，
	// 该顺序即路由选取候选时的推荐顺序。
	List(ctx context.Context, q ChannelQuery) ([]*Channel, error)

	// Count 返回符合条件的渠道总数，用于分页。
	Count(ctx context.Context, q ChannelQuery) (int, error)

	// StatusCounts 按状态分组统计渠道数量，用于仪表盘概览。
	StatusCounts(ctx context.Context) (map[ChannelStatus]int, error)

	// RecordProbeResult 记录一次测活的完整结论（时间、是否通过、延迟、状态码、命中的模型）。
	//
	// 之所以单独一个方法而不复用 Update：测活是高频的后台行为（每轮巡检都会跑），
	// 若走 Update 会把整行配置一并写回，存在"用陈旧副本覆盖管理员刚改的配置"的风险。
	//
	// 手动点「测活」与后台巡检共用这个方法：两者的结论应该落在同一个地方，
	// 否则会出现"后台显示 200ms、点一下测活变成 1800ms"的自相矛盾。
	RecordProbeResult(ctx context.Context, result ChannelProbeResult) error

	// Update 按 ID 更新渠道（不修改创建时间），不存在时返回 ErrChannelNotFound。
	Update(ctx context.Context, ch *Channel) error

	// Delete 按 ID 删除渠道，不存在时返回 ErrChannelNotFound。
	//
	// 说明：M1 采用物理删除；后续若需要保留计费审计关联，会改为软删除。
	Delete(ctx context.Context, id uint64) error
}
