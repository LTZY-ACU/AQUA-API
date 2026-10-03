// 支付对账循环：向支付网关【主动查单】，补偿丢失的回调。
//
// 意图（Why）：
//
//	回调是当前唯一的入账触发器，而回调可能因为域名迁移、网关故障、
//	配置错误而永久丢失——用户已被扣款，订单却永远停在"待支付"或被
//	超时关闭。2026-10-03 生产迁移就真实发生过（notify_base 指向已停机
//	的旧域名，用户支付后不到账）。本循环把"等回调"升级为"等回调 +
//	定期主动问网关"，让迟到的钱有一条自动到账的路。
//
// 流转（Flow）：
//
//	server.Run 起本 goroutine
//	  → 每 5 分钟 ListReconcilable（待支付 + 近 48h 已关闭）
//	  → Registry.QueryOrder 逐笔向网关查单
//	  → 已支付且金额一致 → MarkPaid + CreditOrder + 返利（与回调路径同款幂等语义）
//	  → 网关明确未支付且已到期 → 关闭（防误关：只有网关敢说"没付"才关）
//	  → 查询失败/无法识别/金额不符 → 跳过留待下轮或人工
//
// 扩展（Extend）：
//
//	新通道支持对账：在适配器上实现 payment.Querier 即可（Registry.QueryOrder
//	自动发现）；不支持查单的通道每轮记一条 debug 后跳过。
package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// 对账节奏与追溯窗口。取值考量：
//   - 5 分钟一轮：用户支付后平均 2.5 分钟内被自动补账，体感"稍等就到"；
//     同时对网关的查询压力可忽略（每轮 ≤ 200 条，见 ListReconcilable 的 LIMIT）。
//   - 启动先等 2 分钟：服务刚起时网络/DNS 可能未就绪，首轮查单失败率高且无意义。
//   - 已关闭订单追溯 48 小时：覆盖"付款后收银台挂起、隔天才想起来"的极端场景；
//     更早的关闭单不再自动处理（防止陈年订单反复查单），留人工对账兜底。
const (
	paymentReconcileInterval    = 5 * time.Minute
	paymentReconcileStartDelay  = 2 * time.Minute
	paymentReconcileClosedSince = 48 * time.Hour
)

// startPaymentReconciler 启动支付对账循环（随 ctx 取消而退出）。
func (s *Server) startPaymentReconciler(ctx context.Context) {
	select {
	case <-time.After(paymentReconcileStartDelay):
	case <-ctx.Done():
		return
	}
	s.reconcilePaymentsOnce(ctx)

	ticker := time.NewTicker(paymentReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcilePaymentsOnce(ctx)
		}
	}
}

// reconcilePaymentsOnce 执行一轮对账：查单 → 补账 / 关单。
func (s *Server) reconcilePaymentsOnce(ctx context.Context) {
	// 支付未启用直接返回（每轮现读设置，后台开关即时生效）
	settings, err := model.LoadSiteSettings(ctx, s.deps.Settings)
	if err != nil {
		slog.Warn("支付对账：读取站点设置失败，本轮跳过", "error", err)
		return
	}
	if !settings.Payment.Enabled {
		return
	}

	orders, err := s.deps.Orders.ListReconcilable(ctx, time.Now().Add(-paymentReconcileClosedSince))
	if err != nil {
		slog.Warn("支付对账：查询待对账订单失败", "error", err)
		return
	}
	if len(orders) == 0 {
		return
	}

	var (
		compensated int // 已支付但回调丢失，本轮自动补账
		closed      int // 网关明确未支付且已到期，本轮关闭
		skipped     int // 查询失败/无法识别/金额不符/通道不支持
	)
	now := time.Now()
	for _, order := range orders {
		select {
		case <-ctx.Done():
			return // 服务关闭：剩余订单留待下轮
		default:
		}
		if handled := s.reconcileOneOrder(ctx, order, now); handled == reconcileCompensated {
			compensated++
		} else if handled == reconcileClosed {
			closed++
		} else {
			skipped++
		}
	}
	// 一轮有动作才记日志：静默轮（全部跳过）降为 debug，避免日志刷屏
	if compensated > 0 || closed > 0 {
		slog.Info("支付对账完成", "orders", len(orders),
			"compensated", compensated, "closed", closed, "skipped", skipped)
	} else {
		slog.Debug("支付对账完成（无状态变化）", "orders", len(orders), "skipped", skipped)
	}
}

// reconcileOutcome 单笔订单的对账处置结果。
type reconcileOutcome int

const (
	reconcileSkipped     reconcileOutcome = iota // 无法判定/查询失败，留待下轮
	reconcileCompensated                         // 网关已支付，本轮补账入账
	reconcileClosed                              // 网关明确未支付且到期，本轮关闭
)

// reconcileOneOrder 对账单笔订单，返回处置结果。
//
// 判定标准与回调路径（handlePaymentNotify）完全一致：
//   - Paid 且金额与本地订单一致 → MarkPaid + 入账 + 返利；
//   - 金额不一致 → 只告警不入账（可能是网关数据异常，也绝不给攻击留口子）；
//   - 未支付且已过期 → 关单；未支付未过期 → 跳过（下轮再问）；
//   - 响应无法识别/通道不支持查单 → 跳过。
func (s *Server) reconcileOneOrder(ctx context.Context, order *model.PaymentOrder, now time.Time) reconcileOutcome {
	result, err := s.deps.Payment.QueryOrder(ctx, order.Method, order.TradeNo)
	if err != nil {
		// 不支持查单的通道是常态（Stripe 等），降为 debug；
		// 网络/网关错误是暂态，本轮跳过、下轮重试。
		slog.Debug("支付对账：查单未完成，跳过", "trade_no", order.TradeNo, "error", err)
		return reconcileSkipped
	}
	if !result.Recognized {
		return reconcileSkipped
	}

	if result.Paid {
		// 金额校验与回调同标准：> 0 且等于本地订单金额才入账
		if result.AmountCents <= 0 || result.AmountCents != order.Amount {
			slog.Warn("支付对账：网关已支付但金额不一致，留人工处理",
				"trade_no", order.TradeNo, "user_id", order.UserID)
			return reconcileSkipped
		}
		// payload 标记来源为自动对账：与回调原文可区分，审计可追溯
		payload := fmt.Sprintf("reconciled:auto provider_trade_no=%s", result.ProviderTradeNo)
		transitioned, err := s.deps.Orders.MarkPaid(ctx, order.TradeNo, result.ProviderTradeNo, payload, now)
		if err != nil {
			slog.Warn("支付对账：标记已支付失败，下轮重试", "trade_no", order.TradeNo, "error", err)
			return reconcileSkipped
		}
		if transitioned && order.Status == model.PaymentStatusClosed {
			slog.Warn("支付对账：发现迟到支付（订单此前已超时关闭），已补记为已支付",
				"trade_no", order.TradeNo, "user_id", order.UserID, "amount_cents", order.Amount)
		}
		if err := s.creditOrder(ctx, order.TradeNo); err != nil {
			// 入账失败不回滚状态：订单已标记已支付，CreditOrder 幂等，
			// 下轮对账（或 ListPaidUncredited 启动补偿）会再次尝试入账。
			slog.Warn("支付对账：入账失败，将重试", "trade_no", order.TradeNo, "error", err)
			return reconcileSkipped
		}
		if err := s.rewardReferralOnRecharge(ctx, order); err != nil {
			slog.Warn("支付对账：充值返利失败，将重试", "trade_no", order.TradeNo, "error", err)
		}
		slog.Info("支付对账：已自动补账（回调丢失的支付）",
			"trade_no", order.TradeNo, "user_id", order.UserID, "amount_cents", order.Amount)
		return reconcileCompensated
	}

	// 网关明确未支付：只有已到期的才关（防误关——用户可能正准备付款）
	if order.Status == model.PaymentStatusPending && order.IsExpired(now) {
		if err := s.deps.Orders.UpdateStatus(ctx, order.TradeNo, model.PaymentStatusClosed); err != nil {
			slog.Warn("支付对账：关闭过期订单失败", "trade_no", order.TradeNo, "error", err)
			return reconcileSkipped
		}
		return reconcileClosed
	}
	return reconcileSkipped
}
