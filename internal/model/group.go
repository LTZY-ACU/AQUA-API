// 本文件定义「模型分组」领域模型与仓储接口。
//
// 意图（Why）：
//
//	"分组"是本网关的运营抓手：渠道归属于某个分组，计价规则也按分组区分，
//	于是"给不同人群不同的价格与不同的上游"这件事只需要改分组配置。
//
//	但分组一直只是散落在渠道与价格表里的字符串，没有任何实体。
//	把分组提升为一等实体后，能得到三件事：
//	  1) 管理员能看到"系统里有哪些分组"，而不是靠翻渠道列表去猜；
//	  2) 可以给分组设置【计费倍率】——按客群差异化定价的常见诉求；
//	  3) 分组名写错时能在界面上被检查出来（未知分组不再静默生效）。
//
// 倍率口径（与全项目一致，改动时必须同步迁移注释与前端说明）：
//
//	ratio 是百分比整数：100 = 1.0 倍、150 = 1.5 倍、50 = 0.5 倍。
//	最终额度 = 基础额度 × ratio / 100，向下取整。
//	用整数而不是浮点：额度扣减每天发生几十万次，浮点误差会累积成对不上的账。
//
// 流转（Flow）：
//
//	后台维护：GroupsView → ModelGroupRepository.Create/Update/Delete
//	转发计费：relay.Billing 查价格时同时查倍率 → quota = base × ratio / 100
//	模型广场：按分组聚合展示"该分组下有哪些模型、价格多少"
//
// 扩展（Extend）：
//
//	新增分组维度（如按用户等级自动分组、分组可用模型白名单）时：
//	在本结构体加字段 + 建新迁移加列 + 同步 store/group_repo.go 的列清单三处。
package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 分组相关的领域错误。
var (
	// ErrModelGroupNotFound 表示分组不存在。
	ErrModelGroupNotFound = errors.New("model: 模型分组不存在")
	// ErrModelGroupDuplicated 表示分组名已存在。
	ErrModelGroupDuplicated = errors.New("model: 分组名已存在")
)

// DefaultGroupName 是默认分组名。
//
// 与迁移 0011 中初始化的分组保持一致：未指定分组时一律使用它，
// 因此它必须始终存在（迁移中已用 INSERT OR IGNORE 保证）。
const DefaultGroupName = "default"

// ratioScale 是倍率的换算基数（百分比）。
const ratioScale int64 = 100

// ModelGroup 表示一个模型分组。
type ModelGroup struct {
	ID          uint64 // 主键
	Name        string // 分组标识（小写，被渠道与价格表引用）
	DisplayName string // 展示名（留空时界面回退 Name）
	// Ratio 是计费倍率（百分比整数，100 = 1.0 倍）。
	Ratio int64
	// UnlockMinRechargeCents 是把令牌挂到本分组所需的【累计有效充值】下限（单位：分）。
	//
	// 0 表示无门槛。大于 0 时服务端会在分配分组时校验归属用户是否达标
	// （见 server.resolveTokenGroupName）——这是"低价分组只给大客户"唯一能被
	// 强制执行的依据：分组是用户自选的，没有门槛校验就等于人人可拿最低折扣。
	//
	// 用金额（分）而非额度（quota）做门槛的两个原因：
	//  1) 余额会被消费掉，按余额判定会让"充过 100 元"的用户在用掉一半后失去资格；
	//  2) quota 是内部记账单位，其数值随兑换比例变动，不适合承载业务承诺。
	UnlockMinRechargeCents int64
	// RpmLimit 是本分组【每分钟允许的请求数上限】（RPM，迁移 0045；0 = 不限）。
	//
	// 为什么放在分组而不是令牌上：分组代表"套餐档位"（免费档 / 标准档 / 代理档），
	// RPM 是套餐的一部分；挂到分组上，管理员调档一次即对该档全部令牌生效，
	// 不必逐令牌维护——与倍率、门槛的归属维度保持一致。
	//
	// 它约束的是"调用速率"，与额度墙约束的"总量"互补：总量闸门挡不住短时高频
	// （一个失控令牌几分钟就能吃掉整月毛利），速率闸门正是在这个维度兜底。
	//
	// 计数为【进程内固定 1 分钟窗口】（见 middleware.GroupRPMLimiter），
	// 单实例部署下即精确的每分钟上限；多实例部署时各实例各算一份。
	RpmLimit int
	// AdminOnly 表示本分组【只能由管理员分发】（迁移 0039）。
	//
	// 为什么需要它：UnlockMinRechargeCents 能表达"充够钱自动解锁"，
	// 但表达不了"谁都别想自助拿到，只有后台代建令牌才行"。后者正是**批发价分组**
	// （如"代理拿货"）唯一正确的落地方式——批发价被普通用户自助拿到，
	// 整个价格体系就塌了；而设一个很高的充值门槛又会让小代理被挡在门外。
	//
	// 语义：
	//   true  = 门户的可选分组列表不下发该分组；非管理员即使绕过界面直接用接口
	//           指定它也会被拒；管理员在后台"代客户建令牌"不受限制。
	//   false = 与迁移前完全一致（默认，也是全部历史数据的取值）。
	//
	// 判据必须是【发起请求的人】是否管理员，而不是令牌归属者——
	// 后台代建令牌传的是目标用户的 id，按归属者判定会让代理令牌在后台也建不出来。
	AdminOnly   bool
	Description string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RequiresRechargeUnlock 表示本分组是否设有充值解锁门槛。
func (g *ModelGroup) RequiresRechargeUnlock() bool {
	return g != nil && g.UnlockMinRechargeCents > 0
}

// RequiresAdminGrant 表示本分组是否只能由管理员分发（仅后台可分发的批发价分组）。
func (g *ModelGroup) RequiresAdminGrant() bool {
	return g != nil && g.AdminOnly
}

// Normalize 就地修正"有明确安全默认值"的越界字段（当前仅 RPM 上限）。
//
// 与 Validate 的分工：Validate 拒绝无法自动纠正的非法值（如倍率为 0），
// Normalize 则把可安全解读的输入纠正好。负的 RPM 上限语义显而易见是"不限"，
// 若直接拒绝，管理员每次改分组都要先自己发现并改掉一个负数；
// 归一为 0 既省事，也不会把限制静默放宽（负数本就不是"限制"，而 0 才是"不限"）。
//
// 调用时机：仓储在 Create / Update 写库前调用（见 store/group_repo.go），
// 保证落库的值永远是规范值，读回时无需再判断。
func (g *ModelGroup) Normalize() {
	if g == nil {
		return
	}
	if g.RpmLimit < 0 {
		g.RpmLimit = 0
	}
}

// Validate 校验分组的必要字段。
func (g *ModelGroup) Validate() error {
	name := strings.TrimSpace(g.Name)
	if name == "" {
		return errors.New("分组标识不能为空")
	}
	// 强制小写：否则 "VIP" 与 "vip" 会被当成两个分组，
	// 而渠道里填的是哪一个完全取决于使用者的手滑，排查成本极高。
	if name != strings.ToLower(name) {
		return fmt.Errorf("分组标识只能使用小写字母（当前为 %q）", name)
	}
	if strings.ContainsAny(name, " \t\n/\\,，") {
		return fmt.Errorf("分组标识不能包含空格、逗号或斜杠（当前为 %q）", name)
	}
	if len(name) > 64 {
		return fmt.Errorf("分组标识最多 64 个字符，当前 %d", len(name))
	}
	// 倍率下限 1（0 或负数会让所有调用变成免费或"倒给额度"）
	if g.Ratio <= 0 {
		return fmt.Errorf("计费倍率必须大于 0（100 表示 1.0 倍），当前 %d", g.Ratio)
	}
	// 上限 100000（1000 倍）：超过这个数量级几乎必然是填错了单位
	// （例如把"1.5 倍"写成了 15 万），拦下来比事后追账便宜得多。
	if g.Ratio > 100_000 {
		return fmt.Errorf("计费倍率过大（当前 %d，100 表示 1.0 倍），请检查是否填错单位", g.Ratio)
	}
	// 解锁门槛是金额（分），负值会让"累计充值 >= 负数"恒成立而静默失效，
	// 填 0 才是"无门槛"的正确表达，因此负值一律拒绝。
	if g.UnlockMinRechargeCents < 0 {
		return fmt.Errorf("解锁门槛金额不能为负（当前 %d 分，0 表示无门槛）", g.UnlockMinRechargeCents)
	}
	return nil
}

// Label 返回界面展示名（未设置展示名时回退标识）。
func (g *ModelGroup) Label() string {
	if strings.TrimSpace(g.DisplayName) != "" {
		return g.DisplayName
	}
	return g.Name
}

// ApplyRatio 按本分组的倍率换算额度（向下取整）。
//
// 说明：倍率为 100 时完全不改变数值，因此默认分组不会引入任何误差。
func (g *ModelGroup) ApplyRatio(base int64) int64 {
	if g == nil || base <= 0 {
		return base
	}
	ratio := g.Ratio
	if ratio <= 0 {
		ratio = ratioScale
	}
	return base * ratio / ratioScale
}

// ModelGroupQuery 是分组列表的查询条件。
type ModelGroupQuery struct {
	// EnabledOnly 为 true 时只返回启用的分组。
	EnabledOnly bool
	Limit       int
	Offset      int
}

// ModelGroupRepository 定义分组的持久化操作。
type ModelGroupRepository interface {
	// Create 新增分组，同名冲突时返回 ErrModelGroupDuplicated。
	Create(ctx context.Context, group *ModelGroup) error

	// GetByName 按标识查询，不存在时返回 ErrModelGroupNotFound。
	GetByName(ctx context.Context, name string) (*ModelGroup, error)

	// List 查询分组列表（按名称升序，保证界面顺序稳定）。
	List(ctx context.Context, query ModelGroupQuery) ([]*ModelGroup, error)

	// Count 统计分组数量。
	Count(ctx context.Context, query ModelGroupQuery) (int64, error)

	// Update 按 ID 更新分组。
	//
	// 约定：不改分组标识（name）。标识被渠道与价格表引用，
	// 改名等于让历史配置全部失联；需要改名时应新建分组再迁移。
	Update(ctx context.Context, group *ModelGroup) error

	// Delete 按 ID 删除分组，不存在时返回 ErrModelGroupNotFound。
	//
	// 注意：调用方应自行确认没有渠道/价格仍引用该分组（见 server 层的校验）。
	Delete(ctx context.Context, id uint64) error
}
