// 本文件实现管理端「真实成本对账」接口。
//
// 意图（Why）：
//
//	站长需要回答一个此前答不出来的问题：**这段时间到底赚了还是亏了，赚在哪个维度上**。
//	收入侧的数据是现成的（usage_logs.quota 就是用户实扣额度），成本侧也有进价表
//	（channel_model_costs），但两者从未在同一张表里对齐过——
//	于是"按次计费渠道成本算成 0、面板看着全是利润"这类问题一直只能靠人工估。
//
//	本接口把这两个来源按同一时间窗聚合成一张对账表：请求数 / 收入额度 / 成本额度 /
//	毛利额度 / 毛利率，并支持按 分组 / 渠道 / 模型 三种维度切换视角。
//	金额一律用整数【额度】单位，毛利率只在最后做展示格式化（浮点仅用于展示字符串）。
//
// 口径（与 store.ReconcileUsage 严格一致，改一处必须改另一处的注释）：
//   - 只统计成功请求（2xx/3xx）：失败请求额度已全额退还，既不构成收入也不产生成本；
//   - 收入 = usage_logs.quota（用户实扣额度）；
//   - 成本 = 按 (渠道, 上游模型名) 匹配进价后估算，按次规则走"次数 × 每次单价"；
//   - 未录进价的请求成本按 0 计，但通过 unpriced_requests 显式暴露，绝不与"免费"混为一谈。
//
// 流转（Flow）：
//
//	后台财务页 → GET /api/admin/finance/reconciliation?from=&to=&dim=
//	  └─ store.ReconcileUsage（SQL 聚合 + 进价匹配）→ 本文件的 DTO 装配与控制
//
// 扩展（Extend）：
//
//	新增维度时：在 model 增加 ReconcileDim* 常量与 store.ReconcileUsage 的 keyExpr 分支，
//	本文件无需改动（dim 原样透传）。新增金额字段时同步 DTO 与 totals。
package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
)

// defaultReconcileDays 是未指定时间窗时的默认回溯天数。
//
// 取 30 天：对账是"按月看生意"的动作，一周太短看不清趋势，一季度又太长不便定位。
const defaultReconcileDays = 30

// reconciliationRowDTO 是对账表一行的对外表示。
//
// 金额字段全部是**额度**（内部记账单位），由前端按站点兑换比例折算成人民币展示；
// gross_margin 是唯一按展示口径格式化的字符串（避免把浮点带进金额链路）。
type reconciliationRowDTO struct {
	Dim   string `json:"dim"`   // 维度：group / channel / model
	Key   string `json:"key"`   // 维度取值（分组名 / 渠道 ID 字符串 / 模型名）
	Label string `json:"label"` // 展示标签（渠道维度为渠道名）
	// Requests 是成功请求数；Priced/UnpricedRequests 拆出成本可否估算的部分。
	Requests         int64 `json:"requests"`
	RevenueQuota     int64 `json:"revenue_quota"`
	CostQuota        int64 `json:"cost_quota"`
	GrossProfitQuota int64 `json:"gross_profit_quota"`
	// GrossMargin 是毛利率的展示字符串（如 "36.8%"、"-12.3%"）；收入为 0 时为 "0.0%"。
	GrossMargin      string `json:"gross_margin"`
	PricedRequests   int64  `json:"priced_requests"`
	UnpricedRequests int64  `json:"unpriced_requests"`
}

// reconciliationTotalsDTO 是全部维度合计（避免前端自行累加出现口径不一致）。
type reconciliationTotalsDTO struct {
	Requests         int64  `json:"requests"`
	RevenueQuota     int64  `json:"revenue_quota"`
	CostQuota        int64  `json:"cost_quota"`
	GrossProfitQuota int64  `json:"gross_profit_quota"`
	GrossMargin      string `json:"gross_margin"`
	PricedRequests   int64  `json:"priced_requests"`
	UnpricedRequests int64  `json:"unpriced_requests"`
}

// handleAdminFinanceReconciliation 处理 GET /api/admin/finance/reconciliation。
//
// 参数：
//   - from / to：时间窗（半开区间 [from, to)）。接受 Unix 秒、RFC3339 或
//     "2006-01-02T15:04" / "2006-01-02"（与审计日志接口同一套解析，见 parseAuditTime）；
//   - dim：聚合维度，group（默认）/ channel / model。
func (s *Server) handleAdminFinanceReconciliation(c *gin.Context) {
	if s.deps.Store == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"对账模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	// 先解析 to（默认现在），再由它回推默认起点，保证"未传参数"时时间窗自洽。
	to := time.Now()
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		parsed, err := parseAuditTime(raw)
		if err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
				oai.TypeInvalidRequest, "invalid_to_time")
			return
		}
		to = parsed
	}
	from := to.AddDate(0, 0, -defaultReconcileDays)
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		parsed, err := parseAuditTime(raw)
		if err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
				oai.TypeInvalidRequest, "invalid_from_time")
			return
		}
		from = parsed
	}
	// 明确报错而不是静默交换：参数写反是常见的调用错误，静默纠正会掩盖问题。
	if !to.After(from) {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"结束时间必须晚于开始时间", oai.TypeInvalidRequest, "invalid_time_range")
		return
	}

	dim := strings.TrimSpace(c.DefaultQuery("dim", model.ReconcileDimGroup))
	if !model.IsValidReconcileDim(dim) {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"对账维度非法：可选 group / channel / model", oai.TypeInvalidRequest, "invalid_dim")
		return
	}

	rows, err := s.deps.Store.ReconcileUsage(c.Request.Context(), from, to, dim)
	if err != nil {
		s.respondInternalError(c, "对账聚合失败")
		return
	}

	items := make([]reconciliationRowDTO, 0, len(rows))
	var totals reconciliationTotalsDTO
	for _, row := range rows {
		items = append(items, reconciliationRowDTO{
			Dim:              row.Dim,
			Key:              row.Key,
			Label:            row.Label,
			Requests:         row.Requests,
			RevenueQuota:     row.RevenueQuota,
			CostQuota:        row.CostQuota,
			GrossProfitQuota: row.GrossProfitQuota(),
			GrossMargin:      formatGrossMargin(row.RevenueQuota, row.CostQuota),
			PricedRequests:   row.PricedRequests,
			UnpricedRequests: row.UnpricedRequests,
		})
		totals.Requests += row.Requests
		totals.RevenueQuota += row.RevenueQuota
		totals.CostQuota += row.CostQuota
		totals.PricedRequests += row.PricedRequests
		totals.UnpricedRequests += row.UnpricedRequests
	}
	totals.GrossProfitQuota = totals.RevenueQuota - totals.CostQuota
	totals.GrossMargin = formatGrossMargin(totals.RevenueQuota, totals.CostQuota)

	c.JSON(http.StatusOK, gin.H{
		"dim":    dim,
		"from":   from.Unix(),
		"to":     to.Unix(),
		"items":  items,
		"total":  len(items),
		"totals": totals,
	})
}

// formatGrossMargin 把收入与成本换算成展示用毛利率字符串。
//
// 说明：浮点只在这里出现（最终展示），不参与任何金额累加——
// 金额链路全部走 int64 额度，避免浮点漂移。收入为 0 时按 "0.0%" 展示，
// 而不是除零或一个没有意义的百分比。
func formatGrossMargin(revenue, cost int64) string {
	if revenue <= 0 {
		return "0.0%"
	}
	ratio := float64(revenue-cost) / float64(revenue) * 100
	return strconv.FormatFloat(ratio, 'f', 1, 64) + "%"
}
