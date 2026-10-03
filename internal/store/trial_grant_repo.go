// 本文件是 model.TrialGrantRepository 的 SQL 实现（限时试用额度台账）。
//
// 意图（Why）：
//
//	给全站用户发一笔"会过期"的额度：24 小时内可用，到期自动收回未用完的部分。
//	users.quota 本身是永久池、没有失效概念，所以本文件的做法是——
//	额度照常进 quota（鉴权/预扣/结算这条热路径一行都不用改），
//	另用一张台账记住"这次发了多少、发的时候用户已经用了多少"，
//	到期时就能算出"该收回多少"，而不会误扣用户自己充进来的钱。
//
// 回收额的计算（关键，防资损；与迁移 0040 的注释是同一套口径）：
//
//	约定「试用额先花、自有余额后花」，且多笔试用额之间「先发的先花（FIFO）」。
//	于是对某一笔发放记录：
//	  它的前面几笔还剩多少（carryOver）会先于它被消费掉；
//	  划给它的消费 = clamp(used_quota − 它的发放基准 − carryOver, 0, 它的发放额)；
//	  应回收       = 发放额 − 划给它的消费，再按用户当前剩余额度封顶。
//	没有 used_baseline（发放瞬间的 used_quota 快照）就无从区分花掉的是
//	试用额还是自有余额，会把用户自己充的钱一起扣走 —— 这是本表存在的唯一理由。
//
// 一致性怎么保证：
//
//   - 发放：单事务内「写台账（INSERT ... SELECT 顺带快照 used_quota）→ 加余额」，
//     第一步就是写语句（避免 SQLite 下"先读后写"的锁升级死锁）；
//     重复批次由 (batch, user_id) 唯一索引兜底，冲突即整体回滚，一分钱不会多发；
//   - 回收：以 `status = 在途` 为幂等闸门（先改状态、再动余额），
//     多实例并发回收时只有一方拿到受影响 1 行，不会重复扣款。
//
// 流转（Flow）：
//
//	GrantAll（后台发放）→ trial_grants 落行 + users.quota 增加
//	ReclaimExpired（后台定时）→ 台账置 reclaimed + users.quota 扣回未用完的部分
//	ActiveFor（门户展示）→ 读出"还剩多少、几时过期"
//
// 扩展（Extend）：
//
//	新增发放维度（如只发给新注册用户）：改 GrantAll 里 INSERT ... SELECT 的
//	WHERE 条件即可，回收与展示逻辑无需改动。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// trialGrantRepository 是 model.TrialGrantRepository 的 SQL 实现，并发安全。
type trialGrantRepository struct {
	db *sql.DB
}

// NewTrialGrantRepository 创建限时试用额台账仓储。
func NewTrialGrantRepository(db *sql.DB) model.TrialGrantRepository {
	return &trialGrantRepository{db: db}
}

// trialPendingRow 是 FIFO 记账需要的一行"在效期内"的发放记录。
type trialPendingRow struct {
	id       uint64
	amount   int64 // 发放额
	baseline int64 // 发放瞬间的 used_quota 快照
	expires  int64 // 到期时间（unix 秒）
}

// trialGrantRemaining 按「试用额先花、先发的先花（FIFO）」算出每笔记录的剩余量。
//
// 为什么必须整体算、不能各算各的：同一用户可能同时持有多笔试用额（不同批次），
// "这次消费吃掉了哪一笔"必须有确定口径。只按自己那笔的 baseline 去算，
// 会出现"上一笔早已见底、下一笔却也被判成花光"的少算（我们要少收钱）。
//
// rows 必须按发放先后（id 递增）排列；返回切片与入参一一对应。
func trialGrantRemaining(rows []trialPendingRow, usedQuota int64) []int64 {
	out := make([]int64, len(rows))
	if len(rows) == 0 {
		return out
	}

	firstBaseline := rows[0].baseline // 最早一笔发放时的 used_quota 基准
	var grantedBefore int64           // 前面几笔一共发了多少

	for i, r := range rows {
		// 本笔发放那一刻，前面几笔还剩多少：这部分会先于本笔被消费掉。
		var carryOver int64
		if i > 0 {
			// 从第一笔发放到本笔发放之间，用户一共消费了多少。
			spent := r.baseline - firstBaseline
			if spent < 0 {
				spent = 0
			}
			carryOver = grantedBefore - spent
			if carryOver < 0 {
				carryOver = 0
			}
		}

		consumed := usedQuota - r.baseline - carryOver
		if consumed < 0 {
			consumed = 0
		}
		if consumed > r.amount {
			consumed = r.amount
		}
		out[i] = r.amount - consumed
		grantedBefore += r.amount
	}
	return out
}

// GrantAll 给全站「启用且额度非不限」的用户发放一笔限时试用额。
//
// 实现要点：
//  1. 事务的第一条语句是 INSERT ... SELECT（写），它同时完成两件事——
//     给每位用户写一行台账、并把**发放前**的 used_quota 快照进 used_baseline；
//  2. 随后才给 users.quota 加钱。顺序不能反：反过来快照拿到的就是加钱后
//     的状态，回收时会把用户自己花掉的钱也算进来（少扣，导致我们亏）。
//  3. 重复批次由唯一索引 (batch, user_id) 拦截，冲突即返回 ErrTrialBatchExists，
//     整个事务回滚，不会出现"发了一半"。
func (r *trialGrantRepository) GrantAll(ctx context.Context, req model.TrialGrantRequest) (model.TrialGrantResult, error) {
	req.Normalize()
	if err := req.Validate(); err != nil {
		return model.TrialGrantResult{}, err
	}

	now := time.Now()
	expiresAt := now.Add(req.TTL)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TrialGrantResult{}, fmt.Errorf("store: 开启试用额发放事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 第 1 步（写）：写台账。INNER 条件与下面的加钱语句必须【完全一致】，
	// 否则会出现"有台账没加钱"或"加了钱没台账"的不一致。
	res, err := tx.ExecContext(ctx, `
		INSERT INTO trial_grants
			(user_id, batch, amount, used_baseline, status, expires_at, reclaimed_amount, created_at, reclaimed_at)
		SELECT id, ?, ?, used_quota, ?, ?, 0, ?, 0
		FROM users WHERE status = ? AND quota != ?`,
		req.Batch, req.Amount, string(model.TrialGrantPending), expiresAt.Unix(),
		now.Unix(), int(model.UserStatusEnabled), model.QuotaUnlimited,
	)
	if err != nil {
		if isUniqueViolation(err) {
			// 该批次已经发过了：整批回滚，不做任何发放。
			_ = tx.Rollback()
			return model.TrialGrantResult{}, model.ErrTrialBatchExists
		}
		return model.TrialGrantResult{}, fmt.Errorf("store: 写入试用额台账失败: %w", err)
	}
	recipients, err := res.RowsAffected()
	if err != nil {
		return model.TrialGrantResult{}, fmt.Errorf("store: 读取试用额发放人数失败: %w", err)
	}

	// 第 2 步：给这些用户加钱（条件与上一步逐字一致）。
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET quota = quota + ?, updated_at = ?
		WHERE status = ? AND quota != ?`,
		req.Amount, now.Unix(), int(model.UserStatusEnabled), model.QuotaUnlimited,
	); err != nil {
		return model.TrialGrantResult{}, fmt.Errorf("store: 发放试用额到余额失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return model.TrialGrantResult{}, fmt.Errorf("store: 提交试用额发放事务失败: %w", err)
	}

	return model.TrialGrantResult{
		Batch:      req.Batch,
		Recipients: recipients,
		Amount:     req.Amount,
		ExpiresAt:  expiresAt,
	}, nil
}

// ReclaimExpired 回收"已到期且在效期内"的发放记录中未用完的部分，返回处理条数。
//
// 为什么要把**全部在效期内**的记录都读进来，而不是只读已到期的那些：
// FIFO 记账需要知道"每一笔前面还有多少没花完"，只看已到期的那几行会把
// 顺序算错（少收钱）。所以：先拉全量在效期记录算清各自的剩余，再只对已到期的动手。
//
// 处理顺序（先改状态、再动余额）是幂等的关键：并发回收同一行时，
// 只有把状态从 pending 改成 reclaimed 的那一方拿到受影响 1 行，
// 落败的一方直接跳过，绝不会重复扣款。
func (r *trialGrantRepository) ReclaimExpired(ctx context.Context, now time.Time) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: 开启试用额回收事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 先读后写：把全部在效期记录连同用户当前余额一次读入内存
	// （与 CleanupExpired 同一套写法：读结果集必须先关闭，再做后续写操作）。
	rows, err := tx.QueryContext(ctx, `
		SELECT g.user_id, g.id, g.amount, g.used_baseline, g.expires_at, u.quota, u.used_quota
		FROM trial_grants g JOIN users u ON u.id = g.user_id
		WHERE g.status = ? AND g.expires_at > 0
		ORDER BY g.user_id, g.id`,
		string(model.TrialGrantPending))
	if err != nil {
		return 0, fmt.Errorf("store: 查询在效期内试用额失败: %w", err)
	}

	type userTrial struct {
		quota, used int64
		rows        []trialPendingRow
	}
	byUser := make(map[uint64]*userTrial, 8)
	var order []uint64 // 保持用户出现顺序，便于日志与排查
	for rows.Next() {
		var (
			userID      uint64
			row         trialPendingRow
			quota, used int64
		)
		if err := rows.Scan(&userID, &row.id, &row.amount, &row.baseline, &row.expires, &quota, &used); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("store: 读取在效期内试用额失败: %w", err)
		}
		item, ok := byUser[userID]
		if !ok {
			item = &userTrial{quota: quota, used: used}
			byUser[userID] = item
			order = append(order, userID)
		}
		item.rows = append(item.rows, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("store: 遍历在效期内试用额失败: %w", err)
	}
	_ = rows.Close()

	processed := 0
	reclaimByUser := make(map[uint64]int64, 8)

	for _, userID := range order {
		item := byUser[userID]
		remaining := trialGrantRemaining(item.rows, item.used)

		// 该用户还能扣多少：不限额度或已无剩余时不回写余额（只把台账置终态）。
		var room int64
		if item.quota != model.QuotaUnlimited {
			room = item.quota - item.used
			if room < 0 {
				room = 0
			}
		}

		for i, row := range item.rows {
			if row.expires > now.Unix() {
				continue // 尚未到期：留给下一轮
			}
			reclaim := remaining[i]
			if reclaim > room {
				reclaim = room
			}
			if reclaim < 0 {
				reclaim = 0
			}

			// 幂等闸门：只有成功把状态从 pending 改成 reclaimed 的这一次才动余额。
			res, err := tx.ExecContext(ctx, `
				UPDATE trial_grants SET status = ?, reclaimed_amount = ?, reclaimed_at = ?
				WHERE id = ? AND status = ?`,
				string(model.TrialGrantReclaimed), reclaim, now.Unix(),
				row.id, string(model.TrialGrantPending))
			if err != nil {
				return processed, fmt.Errorf("store: 回收试用额 %d 失败: %w", row.id, err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return processed, fmt.Errorf("store: 读取试用额回收影响行数失败: %w", err)
			}
			if affected == 0 {
				// 已被其它实例回收：跳过，避免重复扣款。
				continue
			}
			processed++
			if reclaim > 0 {
				room -= reclaim
				reclaimByUser[userID] += reclaim
			}
		}
	}

	for userID, reclaim := range reclaimByUser {
		if reclaim <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE users SET quota = quota - ?, updated_at = ?
			WHERE id = ? AND quota != ?`,
			reclaim, now.Unix(), userID, model.QuotaUnlimited,
		); err != nil {
			return processed, fmt.Errorf("store: 回收用户 %d 试用额失败: %w", userID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return processed, fmt.Errorf("store: 提交试用额回收事务失败: %w", err)
	}
	return processed, nil
}

// ActiveFor 返回某用户当前仍有效的试用额汇总。
//
// 同样要把"已到期但回收协程还没跑到"的记录一起读进来参与 FIFO 排序：
// 它们的额度此刻还在用户余额里、也确实会先被消费，漏掉就会把剩余算多或算少。
// 但**返回时不计入**它们——对用户而言那笔已经失效，界面不该再显示。
func (r *trialGrantRepository) ActiveFor(ctx context.Context, userID uint64, now time.Time) (model.TrialActive, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT g.id, g.amount, g.used_baseline, g.expires_at, u.quota, u.used_quota
		FROM trial_grants g JOIN users u ON u.id = g.user_id
		WHERE g.user_id = ? AND g.status = ? AND g.expires_at > 0
		ORDER BY g.id`,
		userID, string(model.TrialGrantPending))
	if err != nil {
		return model.TrialActive{}, fmt.Errorf("store: 查询用户 %d 试用额失败: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()

	var (
		pending   []trialPendingRow
		quota     int64
		used      int64
		unlimited bool
		loaded    bool
	)
	for rows.Next() {
		var row trialPendingRow
		var q, u int64
		if err := rows.Scan(&row.id, &row.amount, &row.baseline, &row.expires, &q, &u); err != nil {
			return model.TrialActive{}, fmt.Errorf("store: 读取用户 %d 试用额失败: %w", userID, err)
		}
		if !loaded {
			loaded = true
			quota, used = q, u
			unlimited = quota == model.QuotaUnlimited
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		return model.TrialActive{}, fmt.Errorf("store: 遍历用户 %d 试用额失败: %w", userID, err)
	}

	remaining := trialGrantRemaining(pending, used)

	var (
		total    int64 // 仍有效的试用额合计
		earliest int64 // 仍有效的那几笔里最早的到期时间（unix 秒）
	)
	for i, row := range pending {
		if row.expires <= now.Unix() || remaining[i] <= 0 {
			continue
		}
		total += remaining[i]
		// 取最早的那个到期时间：那才是用户真正的最后期限。
		if earliest == 0 || row.expires < earliest {
			earliest = row.expires
		}
	}

	// 余额比台账算出来的还少时以余额为准（管理员手工改过额度等情况）。
	if !unlimited && quota >= 0 {
		if room := quota - used; total > room {
			if room < 0 {
				room = 0
			}
			total = room
		}
	}
	if total <= 0 || earliest == 0 {
		return model.TrialActive{}, nil
	}
	return model.TrialActive{Remaining: total, ExpiresAt: time.Unix(earliest, 0)}, nil
}
