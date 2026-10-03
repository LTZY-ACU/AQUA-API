// 本文件实现「邀请返利 + 每日签到」的用户门户接口，以及充值返利的挂载点。
//
// 意图（Why）：
//
//	把邀请与签到的"对外表现"收在一处：
//	  1) GET  /api/user/referral         —— 邀请码、邀请链接、邀请人数、累计返利、签到概况；
//	  2) GET  /api/user/referral/rewards —— 返利明细（财务板块的"返利明细"列表，分页）；
//	  3) GET  /api/user/checkin          —— 今日是否已签、连续天数、累计天数与累计额度；
//	  4) POST /api/user/checkin          —— 执行签到（北京时间当天仅一次）。
//	另外向支付侧暴露一个私有方法 rewardReferralOnRecharge，
//	供"入账成功"的两处调用点（管理员人工确认、支付回调）统一挂载充值返利，
//	避免两条路径各写一份返利逻辑而漏掉幂等。
//
// 流转（Flow）：
//
//	门户页 ReferralView.vue → GET /api/user/referral → 本文件 handleReferralInfo
//	门户页 FinanceView.vue  → GET /api/user/referral/rewards → handleMyReferralRewards
//	                        → POST /api/user/checkin → handleCheckin
//	充值入账成功 → handler_payment 两处调用点 → rewardReferralOnRecharge(order)
//
// 扩展（Extend）：
//
//	新增返利维度（如"邀请满 N 人额外奖励"）：在 rewardReferralOnRecharge 之后
//	追加一段独立的发奖计算（仍走 GrantReward 的幂等路径），不要改动既有分支。
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
	"github.com/LTZY-ACU/ltzy-api/internal/server/middleware"
)

// checkinStatusDTO 是签到状态的对外表示（GET/POST /api/user/checkin 共用）。
type checkinStatusDTO struct {
	// Enabled 是签到功能总开关；false 时前端应隐藏签到按钮。
	Enabled bool `json:"enabled"`
	// DailyQuota 是每次签到发放的额度（0 表示只记天数、不发额度）。
	DailyQuota int64 `json:"daily_quota"`
	// CheckedToday 表示北京时间今天是否已签到。
	CheckedToday bool `json:"checked_today"`
	// StreakDays 是当前连续签到天数。
	StreakDays int `json:"streak_days"`
	// TotalDays 是累计签到天数。
	TotalDays int64 `json:"total_days"`
	// TotalQuota 是累计签到获得额度。
	TotalQuota int64 `json:"total_quota"`
}

// handleReferralInfo 处理 GET /api/user/referral。
//
// 一次返回邀请码、邀请链接、邀请人数、累计返利与签到概况：这些都是同一张
// "我的邀请"页面要展示的内容，拆成多个接口只会让首屏多发几次请求。
func (s *Server) handleReferralInfo(c *gin.Context) {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	ctx := c.Request.Context()

	settings, err := model.LoadSiteSettings(ctx, s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取站点设置失败")
		return
	}

	// 邀请码：老用户（历史数据）此处懒生成并落库，保证始终能拿到。
	code, err := s.deps.Referrals.EnsureInviteCode(ctx, user.ID)
	if err != nil {
		s.respondInternalError(c, "生成邀请码失败")
		return
	}

	invited, err := s.deps.Referrals.CountInvitees(ctx, user.ID)
	if err != nil {
		s.respondInternalError(c, "统计邀请人数失败")
		return
	}
	rewarded, err := s.deps.Referrals.TotalRewardQuota(ctx, user.ID)
	if err != nil {
		s.respondInternalError(c, "统计邀请返利失败")
		return
	}

	checkin, err := s.buildCheckinStatus(ctx, user.ID, settings)
	if err != nil {
		s.respondInternalError(c, "统计签到信息失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"invite_code": code,
		// invite_path 给前端直接拼站点域名用；邀请码做 URL 编码以防将来字符集变化
		"invite_path":          "/register?invite=" + url.QueryEscape(code),
		"invited_count":        invited,
		"register_bonus_quota": settings.Referral.RegisterBonus,
		"total_reward_quota":   rewarded,
		// recharge_ratio 是"被邀请人充值时邀请人可得百分之几"。
		// 下发它的原因：返利比例是邀请人最关心的数字（决定值不值得推广），
		// 页面写死或干脆不写都会让用户以为没有返利；由后端下发则改配置立即生效。
		"recharge_ratio": settings.Referral.RechargeRatio,
		"checkin":        checkin,
	})
}

// referralRewardDTO 是返利明细的对外表示（财务板块的"返利明细"列表用）。
type referralRewardDTO struct {
	ID uint64 `json:"id"`
	// Kind / KindText 是奖励类型代码与中文名（如 recharge / 充值返利）。
	Kind     string `json:"kind"`
	KindText string `json:"kind_text"`
	// Quota 是本次奖励额度（额度是站内计费单位，前端按配置折算成人民币展示）。
	Quota int64 `json:"quota"`
	// Invitee 是触发奖励的用户名，已脱敏（见 maskUsername）。
	Invitee string `json:"invitee"`
	// OrderTradeNo 是充值返利对应的订单号；注册奖为空串。
	OrderTradeNo string `json:"order_trade_no"`
	CreatedAt    int64  `json:"created_at"`
}

// handleMyReferralRewards 处理 GET /api/user/referral/rewards（我的返利明细）。
//
// 为什么要单独开放明细而不再只给"累计返利"一个数字：
// 累计值只能告诉用户"拿了多少"，无法回答"哪一笔、什么时候、谁带来的"，
// 而这三件事正是用户对账时想问的。台账（referral_rewards）本就记录了这些字段，
// 开放列表只是把已有数据读出来，不引入新的写入路径。
func (s *Server) handleMyReferralRewards(c *gin.Context) {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	ctx := c.Request.Context()

	page, size, offset := parsePagination(c)
	total, err := s.deps.Referrals.CountRewards(ctx, user.ID)
	if err != nil {
		s.respondInternalError(c, "统计返利记录失败")
		return
	}
	rewards, err := s.deps.Referrals.ListRewards(ctx, user.ID, size, offset)
	if err != nil {
		s.respondInternalError(c, "查询返利明细失败")
		return
	}

	items := make([]referralRewardDTO, 0, len(rewards))
	for _, reward := range rewards {
		items = append(items, referralRewardDTO{
			ID:           reward.ID,
			Kind:         string(reward.Kind),
			KindText:     reward.Kind.String(),
			Quota:        reward.Quota,
			Invitee:      maskUsername(reward.InviteeName),
			OrderTradeNo: reward.OrderTradeNo,
			CreatedAt:    reward.CreatedAt.Unix(),
		})
	}
	c.JSON(http.StatusOK, newPagedResponse(items, int(total), page, size))
}

// maskUsername 对用户名做展示脱敏。
//
// 为什么脱敏：返利明细会出现在邀请人自己的页面上，而用户名可能是邮箱或手机号。
// 邀请人知道自己邀请了谁，脱敏后的形态（如前两位 + 域名）足以让他对上号，
// 同时避免把被邀请人的完整联系方式长期暴露在别人的账号里。
func maskUsername(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	runes := []rune(name)
	// 邮箱：保留前两位 + *** + @域名，域名是辨识度最高又最不敏感的部分
	if at := strings.LastIndex(name, "@"); at > 0 {
		domain := name[at:]
		if len(runes) > 2 {
			return string(runes[:2]) + "***" + domain
		}
		return "***" + domain
	}
	if len(runes) <= 2 {
		return string(runes[:1]) + "***"
	}
	return string(runes[:2]) + "***"
}

// handleGetCheckin 处理 GET /api/user/checkin。
func (s *Server) handleGetCheckin(c *gin.Context) {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	ctx := c.Request.Context()

	settings, err := model.LoadSiteSettings(ctx, s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取站点设置失败")
		return
	}

	status, err := s.buildCheckinStatus(ctx, user.ID, settings)
	if err != nil {
		s.respondInternalError(c, "统计签到信息失败")
		return
	}
	c.JSON(http.StatusOK, status)
}

// handleCheckin 处理 POST /api/user/checkin。
//
// 拒绝语义：签到关闭时返回 403；当天已签到返回 409（而不是静默成功）——
// 明确告知"今天已签过"能让用户确信点击生效了，也便于前端做按钮禁用态。
func (s *Server) handleCheckin(c *gin.Context) {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	ctx := c.Request.Context()

	settings, err := model.LoadSiteSettings(ctx, s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取站点设置失败")
		return
	}
	if !settings.Referral.CheckinEnabled {
		oai.WriteError(c.Writer, http.StatusForbidden,
			"本站未开放签到", oai.TypePermission, "checkin_disabled")
		return
	}

	// 北京时间当天作为唯一键（与 sitemap 缓存同一时区口径，见 seo.beijingDate）。
	today := beijingDate(time.Now())
	granted, err := s.deps.Referrals.Checkin(ctx, user.ID, today, settings.Referral.CheckinDailyQuota)
	if err != nil {
		s.respondInternalError(c, "签到失败")
		return
	}
	if !granted {
		oai.WriteError(c.Writer, http.StatusConflict,
			"今天已经签到过了，明天再来", oai.TypeInvalidRequest, "already_checked_in")
		return
	}

	// 返回最新状态：前端一次拿到连续天数与累计额度，无需再发一次 GET。
	status, err := s.buildCheckinStatus(ctx, user.ID, settings)
	if err != nil {
		s.respondInternalError(c, "统计签到信息失败")
		return
	}
	c.JSON(http.StatusOK, status)
}

// buildCheckinStatus 组装签到状态（含开关与发放额度，便于前端一次渲染）。
func (s *Server) buildCheckinStatus(ctx context.Context, userID uint64, settings model.SiteSettings) (*checkinStatusDTO, error) {
	stats, err := s.deps.Referrals.CheckinSummary(ctx, userID, beijingDate(time.Now()))
	if err != nil {
		return nil, err
	}
	return &checkinStatusDTO{
		Enabled:      settings.Referral.CheckinEnabled,
		DailyQuota:   settings.Referral.CheckinDailyQuota,
		CheckedToday: stats.CheckedToday,
		StreakDays:   stats.StreakDays,
		TotalDays:    stats.TotalDays,
		TotalQuota:   stats.TotalQuota,
	}, nil
}

// rewardReferralOnRecharge 在"订单入账成功"后给邀请人发放充值返利。
//
// 幂等性由仓储层保证：GrantReward 先插台账（撞唯一约束即跳过）再加额度，
// 因此重复回调、管理员重复确认都不会重复发奖。本方法只负责：
//
//	读设置（未开启返利则直接返回）→ 查订单归属人的邀请人 → 按比例算额度 → 发奖。
//
// 返回值语义：返回 error 时调用方会让支付平台重试（重试是安全的，因为发奖幂等），
// 从而在"设置读取失败/数据库瞬时故障"下实现最终的至少一次发放。
func (s *Server) rewardReferralOnRecharge(ctx context.Context, order *model.PaymentOrder) error {
	if order == nil || order.UserID == 0 || order.TradeNo == "" {
		return nil
	}

	settings, err := model.LoadSiteSettings(ctx, s.deps.Settings)
	if err != nil {
		return fmt.Errorf("server: 读取站点设置失败: %w", err)
	}
	if !settings.Referral.Enabled || settings.Referral.RechargeRatio <= 0 {
		return nil
	}

	inviterID, err := s.deps.Referrals.InviterID(ctx, order.UserID)
	if err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			// 订单归属人不存在属异常数据，但不该阻断支付链路
			return nil
		}
		return fmt.Errorf("server: 查询邀请人失败: %w", err)
	}
	if inviterID == 0 {
		return nil // 该用户不是被邀请而来的，无需返利
	}

	// 比例换算：向下取整（不足 1 个额度的零头舍去，宁可少给不可多给）。
	quota := order.Quota * int64(settings.Referral.RechargeRatio) / 100
	if quota <= 0 {
		return nil
	}

	// 月度上限：返利是从利润里出的纯成本，而 6 折代理档的净利本就薄。
	// 不设上限时，一个高流水代理的返利会逐笔侵蚀掉那点毛利；
	// 超过上限即不再发放，并留一条可观测的日志（否则站长根本不知道返利停了）。
	if capQuota := settings.Referral.MonthlyRewardCapQuota; capQuota > 0 {
		monthStart := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Local)
		spent, err := s.deps.Referrals.TotalRewardQuotaSince(ctx, inviterID, monthStart)
		if err != nil {
			return fmt.Errorf("server: 查询本月已返利额度失败: %w", err)
		}
		if spent+quota > capQuota {
			slog.Warn("邀请返利已达本月上限，本次不再发放",
				"inviter_id", inviterID, "order", order.TradeNo,
				"本月已返", spent, "本次欲返", quota, "上限", capQuota)
			return nil
		}
	}

	reward := &model.ReferralReward{
		InviterID:    inviterID,
		InviteeID:    order.UserID,
		Kind:         model.ReferralKindRecharge,
		Quota:        quota,
		OrderTradeNo: order.TradeNo,
	}
	if _, err := s.deps.Referrals.GrantReward(ctx, reward); err != nil {
		return fmt.Errorf("server: 发放充值返利失败: %w", err)
	}
	return nil
}
