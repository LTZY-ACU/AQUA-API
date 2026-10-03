// 本文件实现「财务记录」板块的汇总接口。
//
// 意图（Why）：
//
//	用户的钱在本站有三个出口：充值进来、返利进来、调用花掉。
//	这些数字此前散落在概览（余额）、充值页（充值记录）、邀请页（累计返利）、
//	日志页（消费明细）四处，用户对账时要来回翻四个页面 ——
//	而"我这段时间的钱去哪儿了"本质上是一个问题，应该在一处回答。
//
//	因此本文件只做一件事：把四个数字一次算好交给前端。
//	（明细列表仍复用既有接口：订单列表、返利明细、调用日志。）
//
//	口径说明（对账时必须一致，否则用户会发现"汇总对不上明细"）：
//	  - RechargedQuota：已支付订单的额度合计，即"当前有效充值额"；
//	  - RewardQuota：邀请奖励台账合计（注册奖 + 充值返利）；
//	  - UsedQuota：用户累计消费，由计费写入，是"花掉了多少"的唯一来源；
//	  - BalanceQuota：用户当前余额（"不限额度"的特殊值 -1 由前端按原样展示）。
//
// 流转（Flow）：
//
//	门户页 FinanceView.vue → GET /api/user/finance → 本文件 handleFinanceSummary
//	  ├─ Orders.SumPaidQuota        已支付额度合计
//	  ├─ Orders.Count               已支付订单笔数（配合合计展示"充了几笔"）
//	  └─ Referrals.TotalRewardQuota 累计返利
//
// 扩展（Extend）：
//
//	新增财务维度（如"累计兑换码领取额度"）时，在本 DTO 加字段并在下方一次装配完，
//	不要在多个接口里各返回一部分 —— 汇总口径必须出自同一处。
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
	"github.com/LTZY-ACU/ltzy-api/internal/server/middleware"
)

// financeSummaryDTO 是财务板块顶部汇总卡的对外表示。
//
// 所有金额字段都是**额度**（站内计费单位），由前端按后端配置的兑换比例
// 折算成人民币展示：比例可能被站长改动，后端不参与展示换算。
type financeSummaryDTO struct {
	// BalanceQuota 当前余额；QuotaUnlimited(-1) 表示不限额度。
	BalanceQuota int64 `json:"balance_quota"`
	// UsedQuota 累计消费额度。
	UsedQuota int64 `json:"used_quota"`
	// RechargedQuota 累计有效充值额度（已支付订单的额度合计）。
	RechargedQuota int64 `json:"recharged_quota"`
	// RechargeCount 已支付订单笔数。
	RechargeCount int64 `json:"recharge_count"`
	// RewardQuota 累计返利额度（邀请注册奖 + 充值返利）。
	RewardQuota int64 `json:"reward_quota"`
}

// handleFinanceSummary 处理 GET /api/user/finance。
//
// 刻意不返回明细：明细各有专门接口（订单/返利/日志）且都要分页，
// 汇总接口只回答"四个数字是多少"，保持轻量、可被任何页面复用。
func (s *Server) handleFinanceSummary(c *gin.Context) {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	ctx := c.Request.Context()

	recharged, err := s.deps.Orders.SumPaidQuota(ctx, user.ID)
	if err != nil {
		s.respondInternalError(c, "统计充值合计失败")
		return
	}
	paidStatus := model.PaymentStatusPaid
	rechargeCount, err := s.deps.Orders.Count(ctx, model.PaymentOrderQuery{
		UserID: user.ID,
		Status: &paidStatus,
	})
	if err != nil {
		s.respondInternalError(c, "统计充值笔数失败")
		return
	}
	rewarded, err := s.deps.Referrals.TotalRewardQuota(ctx, user.ID)
	if err != nil {
		s.respondInternalError(c, "统计返利合计失败")
		return
	}

	c.JSON(http.StatusOK, financeSummaryDTO{
		BalanceQuota:   user.Quota,
		UsedQuota:      user.UsedQuota,
		RechargedQuota: recharged,
		RechargeCount:  rechargeCount,
		RewardQuota:    rewarded,
	})
}
