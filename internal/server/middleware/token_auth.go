// 本文件实现下游令牌的鉴权中间件。
//
// 意图（Why）：
//
//	网关对外的第一道关口：确认"调用者是谁、能不能调用、能调用哪些模型、还有没有额度"。
//	没有鉴权，网关就是一个开放的转发器——任何人拿到地址即可白嫖上游额度，
//	并且所有用量都会记在站长头上。
//
// 流转（Flow）：
//
//	请求 → TokenAuth
//	  ├─ extractAPIKey        从请求头提取令牌明文
//	  ├─ tokens.GetByKey      按摘要索引查库（O(1)，不解密全表）
//	  ├─ EffectiveStatus      结合时间与额度判定令牌自身状态
//	  ├─ users.GetByID        账号级校验：账号是否被禁用
//	  ├─ 解析模型名            白名单校验 + 判断"本次是否计费"（两者都需要模型名）
//	  ├─ 计费判定              不计费（未定价 / 显式免费）→ 完全跳过额度墙
//	  ├─ 额度校验与预留        仅对计费模型：可用额度不足 → 429；额度紧张时预扣
//	  └─ SetToken → c.Next()  放行并把令牌、幂等键与分组写入上下文
//
// 顺序为什么必须是这样（别随意调换）：
//
//	额度校验必须在"判断本次是否计费"之后。否则额度为 0 的用户连免费模型
//	都会被拦成 429，站长为了让免费模型可用，只能把用户额度设成"不限"（-1）
//	——用一个哨兵值掩盖校验顺序错了这一真正原因（历史上就是这么来的）。
//
// 扩展（Extend）：
//
//	新增校验维度（IP 白名单、RPM 限制）时：插在"模型白名单校验"之后、
//	  计费判定之前，并保持"先廉价判定、后昂贵判定"的顺序（如先查内存/缓存，再读请求体）。
//	新增鉴权方式（如 JWT）：新建同级文件，复用 SetToken 的上下文约定。
//	新增预留的例外情形：改 tryReserveQuota，务必保持"不计费模型跳过预留"
//	  这一条（否则免费模型会被额度墙挡住）。
package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"gitee.com/xiaosu4610/aqua-api/internal/model"
	"gitee.com/xiaosu4610/aqua-api/internal/oai"
	"gitee.com/xiaosu4610/aqua-api/internal/reqctx"
)

// bearerPrefix 是 Authorization 头中令牌的标准前缀。
const bearerPrefix = "Bearer "

// trustQuotaBypassThreshold 是「信任额度旁路」的阈值（内部额度单位）。
//
// 当账号【可用额度】不低于该阈值时跳过预留（不写预留台账、不预扣额度），
// 只做响应后的常规扣费。取舍如下：
//   - 减少写库：额度充足的账号通常占多数，为其每次调用都写一条预留记录
//     会显著放大数据库写压力；
//   - 风险可控：可用额度远大于单次调用成本时，最坏情况的"超支"也不会造成
//     实质资损（这类账号本就额度充足）；
//   - 额度紧张的账号（低于阈值）仍走严格预扣，并发超支漏洞依旧被堵住。
//
// 取值 1_000_000 与"每 1M token 对应的额度"同量级，即"大致够一次百万 token 级调用"。
const trustQuotaBypassThreshold int64 = 1_000_000

// reservationTTL 是预留的在途有效期。
//
// 必须大于上游首字节超时（relay.UpstreamTimeout = 300 秒）并留足余量，
// 否则一个"慢但正常"的请求会在结算前就被当成陈旧预留回收。
const reservationTTL = 15 * time.Minute

// QuotaReserver 是鉴权层做「额度预留」所需的最小能力集，由计费组件（relay.Billing）实现。
//
// 在消费方定义接口（而非依赖具体实现），是为了让本包不依赖 internal/relay，
// 保持"鉴权只关心能否预留，不关心价格怎么算"的分层。
type QuotaReserver interface {
	// EstimateReserve 估算一次调用的预留额度；priced=false 表示该模型不计费（应跳过预留）。
	// group 为本次请求的分组；空字符串表示未指定，由计费组件回退到默认分组。
	EstimateReserve(ctx context.Context, group, modelName string, promptBytes int) (amount int64, priced bool)
	// Reserve 预扣额度；可用额度不足时返回 model.ErrQuotaInsufficient。
	Reserve(ctx context.Context, req model.ReserveRequest) (*model.QuotaReservation, error)
	// PendingReserved 返回某用户在途预留的合计额度。
	//
	// 现在只用于"额度耗尽"的错误文案（给站长定位问题的诊断数字），
	// 不再参与可用额度计算——预留落台账时就已同步进 used_quota，
	// 再减一次会把同一份额度扣两遍（见 model.User.RemainingQuota）。
	// 因此它只在【即将返回 429】的失败分支才查询，热路径零额外读库。
	PendingReserved(ctx context.Context, userID uint64) (int64, error)
	// Release 退还预留；幂等（对不存在/已结算的预留是空操作）。
	//
	// 由本中间件在兜底释放时调用：请求走不到结算（敏感词拦截、参数校验失败、
	// 任务提交前早退）时，额度不能一直占到 TTL 到期才自动回收。
	Release(ctx context.Context, requestID string) error
}

// TokenAuth 返回校验下游令牌的 gin 中间件。
//
// 参数：
//   - tokens 为令牌仓储；
//   - users 为用户仓储（用于账号级额度校验），可为 nil（此时跳过该层校验）；
//   - reservers 为可选的额度预留器（通常传入计费组件）。不传时不做预留，
//     保持"只在响应后扣费"的旧行为——便于测试与"仅统计不限制"的部署形态。
//
// 三者都由 main 装配后注入，便于替换实现与单元测试。
func TokenAuth(tokens model.TokenRepository, users model.UserRepository, reservers ...QuotaReserver) gin.HandlerFunc {
	var reserver QuotaReserver
	if len(reservers) > 0 {
		reserver = reservers[0]
	}

	return func(c *gin.Context) {
		// ── 步骤 1：提取令牌 ────────────────────────────────────
		rawKey := extractAPIKey(c.Request)
		if rawKey == "" {
			abortWithErrorKey(c, http.StatusUnauthorized,
				"auth.missing_token", oai.TypeAuthentication, oai.CodeMissingAPIKey)
			return
		}

		// ── 步骤 2：查库校验令牌是否存在 ────────────────────────
		// 实现上按 key_hash 唯一索引查找，不会解密全表，因此该步骤开销很低。
		token, err := tokens.GetByKey(c.Request.Context(), rawKey)
		if err != nil {
			if errors.Is(err, model.ErrTokenNotFound) {
				// 统一回复"无效"而不区分"不存在"与"格式错误"，
				// 避免向攻击者提供可用于枚举有效令牌的差异信息。
				abortWithErrorKey(c, http.StatusUnauthorized,
					"auth.invalid_token", oai.TypeAuthentication, oai.CodeInvalidAPIKey)
				return
			}
			// 仓储故障：属于网关内部问题，不暴露细节
			abortWithError(c, http.StatusInternalServerError,
				"网关内部错误", oai.TypeServer, oai.CodeInternal)
			return
		}

		// ── 步骤 3：状态判定（结合当前时间与额度）──────────────
		// 注意：这里用 EffectiveStatus 而非 token.Status。
		// 数据库中的 status 只记录管理员意图（启用/手动禁用），
		// "是否过期""额度是否耗尽"是随时间变化的事实，必须实时计算，
		// 否则会出现"令牌已过期但仍可调用"的安全漏洞。
		now := time.Now()
		switch token.EffectiveStatus(now) {
		case model.TokenStatusDisabled:
			abortWithErrorKey(c, http.StatusForbidden,
				"auth.token_disabled", oai.TypePermission, oai.CodeTokenDisabled)
			return
		case model.TokenStatusExpired:
			abortWithErrorKey(c, http.StatusUnauthorized,
				"auth.token_expired", oai.TypeAuthentication, oai.CodeTokenExpired)
			return
		case model.TokenStatusExhausted:
			// 429 而非 403：客户端按"稍后重试/更换令牌"处理更自然
			abortWithErrorKey(c, http.StatusTooManyRequests,
				"quota.token_exhausted", oai.TypeRateLimit, oai.CodeInsufficientQuota)
			return
		}

		// 解析令牌的分组：令牌可指定走某个分组的渠道、按该分组的价格与倍率计费。
		//
		// 为空表示令牌未指定分组，由转发层与计费层各自回退到默认分组——
		// 中间件不感知"默认分组"是什么，只负责把令牌自身的分组原样传下去，
		// 保持"只传递、不判断"的职责（路由与计费规则都不该出现在鉴权层）。
		tokenGroup := token.EffectiveGroupName("")

		// ── 步骤 4：查询用户（账号级上限的载体）────────────────
		//
		// 为什么令牌之外还要查用户额度：令牌是"发给某个用户的凭据"，
		// 用户额度才是账号级上限。若只校验令牌额度，用户可以随手新建
		// 若干个令牌来绕过总量限制——限额就形同虚设。
		//
		// 注意：这里只查用户、不做额度判定。额度判定被后移到【步骤 7】，
		// 因为只有先拿到模型名才能知道"这次调用是否要花钱"（见步骤 5、6）。
		var owner *model.User
		if users != nil && token.OwnerID > 0 {
			owner, err = users.GetByID(c.Request.Context(), token.OwnerID)
			if err != nil {
				if errors.Is(err, model.ErrUserNotFound) {
					// 令牌归属的用户已被删除：令牌本身应视为失效
					abortWithErrorKey(c, http.StatusUnauthorized,
						"auth.token_revoked", oai.TypeAuthentication, oai.CodeInvalidAPIKey)
					return
				}
				abortWithError(c, http.StatusInternalServerError,
					"网关内部错误", oai.TypeServer, oai.CodeInternal)
				return
			}

			if !owner.IsActive() {
				abortWithErrorKey(c, http.StatusForbidden,
					"auth.account_disabled", oai.TypePermission, oai.CodeTokenDisabled)
				return
			}
		}

		// ── 步骤 5：解析模型名（白名单与"是否计费"都要用它）──────
		//
		// 顺序说明（本次修复的关键）：模型名必须在【额度校验之前】拿到。
		// 只有知道是哪个模型，才能判断"这次调用要不要花钱"；
		// 而免费模型必须能被额度为 0 的用户正常调用。
		//
		// 还要先判断"本次请求是否根本没有请求体"：GET/HEAD 这类只读元数据端点
		// （GET /v1/models 拉模型清单、GET /v1/tasks 查异步任务）没有 model 字段可言。
		// 它们既不涉及模型白名单，也不产生任何上游调用与费用，因此
		// 【既不校验白名单、也不走额度墙】。否则会出现两个都真实发生过的荒谬后果：
		//   · 带模型白名单的令牌拉模型清单 → 400「请求体不是合法的 JSON」；
		//   · 余额为 0 的用户连"有哪些模型可用"都看不到 → 429。
		// 结果是用户根本接不进来，而站长从日志里只会看到一串 400/429。
		bodyless := isBodylessRequest(c.Request)

		//   - 白名单非空时【必须】拿到模型名，拿不到就按错误响应返回；
		//   - 白名单为空时，为判断计费而【尽力】读取模型名：
		//     读不到（请求体非法/缺 model/超限）不报错，但也不豁免额度墙（保守），
		//     畸形请求随后会在转发阶段被拒绝，不存在"靠畸形请求白嫖计费模型"的可能。
		//
		// 模型名来源优先级（安全审计 P1 修复，2026-09-28）：Gemini 协议的模型名
		// 在 URL 路径里（/v1beta/models/{model}:generateContent），body 中没有
		// model 字段；必须先看路径，未命中再回退到请求体。若只看 body，
		// Gemini 请求会"拿不到模型名"→ 白名单失效 + 跳过额度预留（并发超支）。
		var (
			modelName   string
			promptBytes int
		)
		pathModel, fromPath := peekModelFromGeminiPath(c.Request.URL.Path)
		switch {
		case bodyless:
			// 无请求体的元数据请求：没有模型需要校验，也不产生费用
		case fromPath && len(token.Models) > 0:
			// Gemini 请求 + 白名单令牌：模型名来自路径，无需读请求体
			modelName = pathModel
			if !token.AllowsModel(modelName) {
				abortWithErrorKey(c, http.StatusForbidden,
					"model.not_allowed", oai.TypePermission, oai.CodeModelNotAllowed)
				return
			}
		case len(token.Models) > 0:
			name, size, err := peekModelFromBody(c)
			if err != nil {
				writeModelBodyError(c, err)
				return
			}
			modelName, promptBytes = name, size
			if !token.AllowsModel(modelName) {
				abortWithErrorKey(c, http.StatusForbidden,
					"model.not_allowed", oai.TypePermission, oai.CodeModelNotAllowed)
				return
			}
		case fromPath:
			// Gemini 请求 + 无白名单：模型名来自路径（供计费判定与额度预留）
			modelName = pathModel
		case owner != nil && reserver != nil && owner.Quota != model.QuotaUnlimited:
			// 只有"额度有限、可能被额度墙拦住"的账号才需要为判断计费而读请求体：
			// 不限额度的账号无论如何都会放行，读它纯属浪费（保持"绝大多数请求零额外开销"）。
			if name, size, err := peekModelFromBody(c); err == nil {
				modelName, promptBytes = name, size
			}
		}

		// ── 步骤 6：判断本次调用是否计费 ────────────────────────
		//
		// 这是"免费模型能不能被 0 额度用户调用"的判定点，也是本次修复的核心：
		//
		//	明确不计费（未命中任何计价规则，或规则被【显式设为免费】）
		//	  → 既不做额度校验、也不做额度预留，直接放行；
		//	计费（或因拿不到模型名而无法判断）
		//	  → 沿用严格的"可用额度 = 总额度 − 已用"判定（见步骤 7）。
		//
		// 为什么必须这样分：若把额度校验放在判断计费之前，0 额度用户连免费模型
		// 都会被拦成 429，站长为了让免费模型可用，只能把用户额度设成"不限"(-1)
		// ——那等于用一个哨兵值掩盖"校验顺序错了"这个真正原因。
		var (
			reserveAmount int64
			modelPriced   bool
		)
		if reserver != nil && modelName != "" {
			reserveAmount, modelPriced = reserver.EstimateReserve(
				c.Request.Context(), tokenGroup, modelName, promptBytes)
		}

		// 语料共建的特殊福利账户：命中"用户 × 模型"白名单时视同免费——
		// 既不校验额度、也不创建预留，与"未定价 / 显式免费"走完全相同的分支。
		//
		// 为什么必须在这里判：EstimateReserve 的入参里没有用户，
		// "按人免费"在计费组件内部无从判定；而此处同时拿得到 owner 与 modelName，
		// 是唯一自然的判定点。判定失败（无此能力）等同于"本站没有福利账户"。
		if modelPriced && owner != nil && modelName != "" {
			if checker, ok := reserver.(interface {
				IsFreeForUser(userID uint64, model string) bool
			}); ok && checker.IsFreeForUser(owner.ID, modelName) {
				modelPriced = false
				reserveAmount = 0
			}
		}

		exemptFromQuota := reserver != nil && modelName != "" && !modelPriced

		// ── 步骤 7：账号级额度校验（仅对计费模型生效）──────────
		//
		// 可用额度 = 总额度 − 已用。【不要】再减"在途预留"：预留落台账的同时
		// 额度就已经加进 used_quota（见 store/quota_repo.go Reserve），在途部分
		// 已包含在 UsedQuota 里，再减一遍等于同一份额度扣两次——额度 10000、
		// 预扣 5000 时再发一个请求会算出 0 而被误判 429（实际还剩 5000）。
		// 并发超支由 Reserve 的原子条件更新（quota - used_quota >= amount，
		// 不足则整笔回滚）兜底，不依赖这里的减项。
		//
		// 两个容易写错的地方（都曾真实踩过）：
		//  1) 必须显式排除「不限额度」：它的 RemainingQuota() 返回 -1，
		//     若直接拿去比较大小，会把所有不限额度的账号全部拦死；
		//  2) 判定要用 <= 0 而不是 == 0：已用超过总额度时剩余为负数，
		//     只判 0 会把"已经超额"的账号放行。
		needReserve := false
		// bodyless：无请求体的只读元数据请求（GET /v1/models 等）跳过额度墙。
		// 它们不产生任何费用，"余额为 0 就不让看模型清单"只会把用户挡在门外。
		if owner != nil && !bodyless && !exemptFromQuota {
			if owner.Quota != model.QuotaUnlimited && owner.RemainingQuota() <= 0 {
				// 报错里带上具体数值：使用者转述给站长时，"额度 0 / 已用 0"
				// 一眼就能定位到是"默认额度没配"，而不是"上游限流"。
				// 在途预留只在这里查一次：诊断信息属于失败路径，不值得热路径每次读库。
				pending := int64(0)
				if reserver != nil {
					if p, perr := reserver.PendingReserved(c.Request.Context(), owner.ID); perr == nil {
						pending = p
					} else {
						slog.Warn("查询在途预留失败，错误文案中省略该数字",
							"error", perr, "user_id", owner.ID)
					}
				}
				abortWithErrorKey(c, http.StatusTooManyRequests,
					"quota.account_exhausted", oai.TypeRateLimit, oai.CodeInsufficientQuota,
					owner.Quota, owner.UsedQuota, pending)
				return
			}

			needReserve = reserver != nil && owner.Quota != model.QuotaUnlimited
		}

		// ── 步骤 8：额度预留（避免并发超支）────────────────────
		requestID := ""
		if needReserve && modelName != "" {
			id, ok := tryReserveQuota(c, reserver, token, owner,
				modelName, reserveAmount)
			if !ok {
				// 额度不足：tryReserveQuota 已写出 429
				return
			}
			requestID = id
		}

		// ── 步骤 6：放行 ────────────────────────────────────────
		SetToken(c, token)

		// 把调用者身份与分组写入请求 context，供转发引擎按分组选渠道/计费，
		// 并在结束时落调用日志 / 结算预留。
		// 用标准库 context 而非 gin 上下文，是为了让 relay 不必依赖 Web 框架。
		// RequestID 仅在实际做了预留时非空，转发结束后据此结算或退还。
		reqCtx := reqctx.WithIdentity(c.Request.Context(), reqctx.Identity{
			UserID:    token.OwnerID,
			TokenID:   token.ID,
			RequestID: requestID,
		})
		reqCtx = reqctx.WithGroup(reqCtx, tokenGroup)

		// 预留一旦产生就必须有人闭环：正常由 relay（结算/退还）或任务服务
		// （提交即退还）负责，但敏感词拦截、参数校验失败、任务提交前早退等
		// 路径根本走不到结算——额度会一直占到 15 分钟 TTL 到期才被回收，
		// 期间用户看到的是"有钱却用不了"。这里加一道 defer 兜底。
		//
		// 闭环标记由结算方在 Settle/Release 成功后置位（reqctx.ReservationGuard），
		// 已结算时本兜底跳过，热路径零额外写库；标记缺失（如某些路径丢了 ctx 值）
		// 也不会出错——Release 幂等，对已结算的预留是空操作。
		if requestID != "" {
			var guard *reqctx.ReservationGuard
			reqCtx, guard = reqctx.WithReservationGuard(reqCtx)
			defer func() {
				if guard.Closed() {
					return
				}
				// 用独立 context：此时请求 context 可能已被取消（客户端断开、
				// 上游超时），沿用它会让这次释放失败并拖到 TTL 才回收。
				releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := reserver.Release(releaseCtx, requestID); err != nil {
					slog.Warn("兜底退还预留额度失败（将等待 TTL 到期由清理任务回收）",
						"error", err, "request_id", requestID, "user_id", token.OwnerID)
				}
			}()
		}

		c.Request = c.Request.WithContext(reqCtx)

		c.Next()
	}
}

// tryReserveQuota 尝试为本次调用预扣额度。
//
// amount 由调用方（鉴权主流程）算出并传入，而不是在这里重新估算：
// 同一个金额既用于"要不要走额度墙"的判定，也用于预留，
// 分成两次估算会让两条判断有机会基于不同的价格快照，从而出现
// "判定说免费、预留却扣钱"这类自相矛盾的行为。
//
// 返回的第二个值为 false 表示已写出错误响应（额度不足），调用方应立即返回。
// 返回空 requestID 且 true 表示"本次不预留"（不计费模型 / 信任额度旁路 / 台账降级）。
func tryReserveQuota(c *gin.Context, reserver QuotaReserver, token *model.Token,
	owner *model.User, modelName string, amount int64) (string, bool) {
	ctx := c.Request.Context()

	if amount <= 0 {
		// 【例外一】该模型不计费（未命中计价规则，或被显式设为免费）：金额为 0 就无需预留。
		// 调用方已在额度校验之前据此放行（免费模型对 0 额度用户也开放），
		// 这里是第二道闸门，确保"没有金额就一定不写预留台账"。
		return "", true
	}

	// 【例外二】信任额度旁路：可用额度充足时跳过预留，减少一次写库。
	//
	// 同样只看"总额度 − 已用"：在途预留已计入 used_quota（见 model.User.RemainingQuota）。
	if owner.RemainingQuota() >= trustQuotaBypassThreshold {
		return "", true
	}

	requestID := model.NewRequestID()
	if _, err := reserver.Reserve(ctx, model.ReserveRequest{
		RequestID: requestID,
		UserID:    owner.ID,
		TokenID:   token.ID,
		Amount:    amount,
		TTL:       reservationTTL,
	}); err != nil {
		if errors.Is(err, model.ErrQuotaInsufficient) {
			// 原子条件更新才是并发闸门：这里显示的"剩余"只是判断瞬间的快照，
			// 不参与判定（判定已由 Reserve 内部完成），仅供使用者定位问题。
			available := owner.RemainingQuota()
			if available < 0 {
				available = 0
			}
			abortWithErrorKey(c, http.StatusTooManyRequests,
				"quota.insufficient", oai.TypeRateLimit, oai.CodeInsufficientQuota,
				available, amount)
			return "", false
		}
		// 台账故障：不因一次记账故障阻断全部调用，降级为"本次不预留"并记错误级别日志。
		slog.Error("额度预留失败，本次未预留（存在超支风险）",
			"error", err, "user_id", owner.ID, "token_id", token.ID, "model", modelName)
		return "", true
	}
	return requestID, true
}

// isBodylessRequest 判断本次请求是否没有请求体。
//
// 判据刻意用 HTTP 方法而不是"读出来是空的"：
//   - GET / HEAD 在语义上就没有请求体，其中的 /v1 端点（/v1/models、/v1/tasks）
//     都是只读元数据，不产生任何上游调用与费用；
//   - POST 即使体为空也应走原有的解析错误路径（错误信息保持一致，
//     也避免"空体 POST 被当成元数据请求"而绕过额度墙）。
func isBodylessRequest(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

// peekModelFromBody 读取请求体并取出 model 名与请求体长度。
//
// 返回值 err 非 nil 时表示无法确定模型名（请求体超限 / 非法 JSON / 缺 model）；
// 是否把它变成错误响应由调用方决定（白名单校验必须报错，额度预留可跳过）。
//
// oai.ReadBody 会还原请求体，因此鉴权之后转发阶段仍能读到完整内容——
// 这是本中间件与转发能共存的关键。
func peekModelFromBody(c *gin.Context) (string, int, error) {
	body, err := oai.ReadBody(c.Request)
	if err != nil {
		return "", 0, err
	}
	modelName, err := oai.PeekModel(body)
	if err != nil {
		return "", 0, err
	}
	return modelName, len(body), nil
}

// peekModelFromGeminiPath 从 Gemini 协议的请求路径解析模型名。
//
// 为什么需要它（安全审计 P1，2026-09-28）：Gemini 的模型名在 URL 路径里
// （/v1beta/models/{model}:generateContent），请求体中没有 model 字段。
// 此前本中间件只从 body 取模型名，导致两条真实缺陷：
//
//  1. 带模型白名单的令牌调 Gemini → 报 ErrMissingModel → 400（白名单失效）；
//  2. 有限额度的用户调 Gemini → modelName 为空 → 跳过额度预留（见步骤 6/8），
//     只剩一次"可用额度>0"的粗校验——并发请求全部在校验后、扣费前通过，
//     最终把额度刷成负数（并发超支）。
//
// 因此模型名的取值优先级为：Gemini 路径 → 请求体 body。路径未命中（非 Gemini
// 请求）时返回 ok=false，调用方回退到 peekModelFromBody，行为与旧版一致。
func peekModelFromGeminiPath(path string) (string, bool) {
	const marker = "/models/"
	idx := strings.Index(path, marker)
	if idx < 0 {
		return "", false
	}
	rest := path[idx+len(marker):]
	if colon := strings.Index(rest, ":"); colon >= 0 {
		rest = rest[:colon]
	}
	model := strings.TrimSpace(rest)
	if model == "" {
		return "", false
	}
	return model, true
}

// writeModelBodyError 把"读取 / 解析请求体"的错误映射为 HTTP 响应。
func writeModelBodyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, oai.ErrRequestTooLarge):
		abortWithErrorKey(c, http.StatusRequestEntityTooLarge,
			"request.too_large", oai.TypeInvalidRequest, oai.CodeRequestTooLarge)
	case errors.Is(err, oai.ErrMissingModel):
		abortWithErrorKey(c, http.StatusBadRequest,
			"request.missing_model", oai.TypeInvalidRequest, oai.CodeMissingModel)
	default:
		abortWithErrorKey(c, http.StatusBadRequest,
			"request.malformed_json", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
	}
}

// extractAPIKey 从请求头中提取令牌明文。
//
// 支持的两种形式（覆盖主流客户端的默认行为）：
//   - Authorization: Bearer sk-xxxx   —— OpenAI SDK 的默认方式
//   - x-api-key: sk-xxxx              —— Anthropic SDK 的默认方式
//
// 安全考量：刻意【不支持】从 URL 查询参数读取令牌。
// 查询参数会出现在访问日志、浏览器历史与 Referer 头中，极易泄露凭据。
func extractAPIKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if key, found := strings.CutPrefix(auth, bearerPrefix); found {
			return strings.TrimSpace(key)
		}
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

// abortWithError 以 OpenAI 兼容格式返回错误并中止后续处理。
//
// 必须调用 c.Abort()：否则 gin 会继续执行同一路由上的后续处理器，
// 导致既返回了错误、又执行了业务逻辑（可能产生额外费用）。
//
// 本函数用于「运维/内部错误」等无需本地化的文案（保持中文，便于日志检索）。
func abortWithError(c *gin.Context, status int, message, errType, code string) {
	oai.WriteError(c.Writer, status, message, errType, code)
	c.Abort()
}

// abortWithErrorKey 是 abortWithError 的多语言版本：按语义化键取词条后输出。
//
// locale 取自 locale 中间件写入的请求 context；未携带 Accept-Language 时为中文。
// args 为可选格式化参数（词条含 %d 等占位符时使用）。
//
// 仅用于「面向最终用户、会被展示」的错误；内部错误请用 abortWithError 保持中文。
func abortWithErrorKey(c *gin.Context, status int, key, errType, code string, args ...any) {
	oai.WriteErrorKey(c.Writer, status, key, errType, code, reqctx.Locale(c.Request.Context()), args...)
	c.Abort()
}
