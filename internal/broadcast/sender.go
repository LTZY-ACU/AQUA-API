// Package broadcast 负责「全站通知邮件」的群发执行。
//
// 意图（Why）：
//
//	群发与单封发信的本质区别是【时间长、量大、不能重来】，因此本包只做三件事，
//	并把它们做扎实：
//	  1) 名单入队：把符合条件、且邮箱非空的用户逐人写进收件人明细（空邮箱跳过、
//	     同一邮箱只留一条）；
//	  2) 节流发送：按固定间隔逐封发送，每批之间停顿，避免瞬时数百个 SMTP 连接
//	     触发服务商限流（项目此前没有任何全站发信总量控制）；
//	  3) 可停可续：发送前逐人确认状态、每封之前确认批次是否被停止，
//	     进程重启后从未发的人继续 —— 已发过的绝不会被再发一次。
//
//	为什么不用"循环一口气发完"：中途任何一次中断（部署、重启、网络抖动）
//	都会让"发到谁了"变成未知数；而重复投递是投诉与垃圾邮件评分上升的首要原因。
//
// 流转（Flow）：
//
//	server.handleCreateBroadcast
//	  → Enqueue（分页枚举用户 → AddRecipients）
//	  → Start（后台 goroutine）
//	      → Run：NextPending → Mailer.Send → MarkRecipient → 每封前查一次状态
//	  → 进程启动时 ResumeAll（串行续发所有未完成批次）
//
// 扩展（Extend）：
//
//	想改发送节奏：调下面三个节流常量即可（间隔 / 每批封数 / 批间停顿）。
//	想按人群筛选：改 Enqueue 收到的 model.UserQuery（调用方决定人群），
//	明细表与发送流程都不需要改。
package broadcast

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// 发送节奏参数（改这三个值就能整体调节奏）。
//
// 取值理由：
//   - interval 2 秒/封：单封约 1 秒（SMTP 往返 + 落库），加上间隔后约 3 秒/封，
//     每小时约 1200 封，远低于任何服务商的默认限流阈值；
//   - batchSize 50 + batchPause 30 秒：每 50 封"歇一口气"，避免长时间稳定高压
//     被判为批量投递（也留出人工发现异常并点"停止"的窗口）。
const (
	defaultInterval   = 2 * time.Second
	defaultBatchSize  = 50
	defaultBatchPause = 30 * time.Second

	// enqueuePageSize 是枚举用户时的分页大小，必须 ≤ 用户仓储的单页上限（100）。
	enqueuePageSize = 100
)

// Mailer 是本包对邮件发送器的最小依赖。
//
// 用接口而不是直接依赖 *mailer.Sender：mailer 包刻意不做重试与队列
// （见其文件头说明），本包需要一个可注入的假实现来测"失败也落库、不重发"。
type Mailer interface {
	Send(ctx context.Context, to, subject, htmlBody string) error
}

// Sender 是群发执行器，并发安全。
type Sender struct {
	broadcasts model.EmailBroadcastRepository
	users      model.UserRepository
	mailer     Mailer

	interval   time.Duration
	batchSize  int
	batchPause time.Duration

	// mu / running 保证"同一批次不会被两个 goroutine 同时发送"。
	//
	// 为什么必须是进程内状态而不只看数据库状态：后台接口可能被连点两次，
	// 两次都会读到 status=running 并"继续发"，结果是同一批用户各收到两封。
	mu      sync.Mutex
	running map[uint64]struct{}
}

// Options 是发送节奏参数。
//
// 零值表示"用默认值"（见下面的节流常量），因此生产代码只需传 Options{}；
// 测试可以传极小的间隔与批大小，让"跨批处理、批间停顿"的分支在毫秒级跑完。
type Options struct {
	// Interval 是每封邮件之间的间隔。
	Interval time.Duration
	// BatchSize 是每批处理的封数（每批结束落库一次进度）。
	BatchSize int
	// BatchPause 是批与批之间的停顿。
	BatchPause time.Duration
}

// New 创建群发执行器。
func New(broadcasts model.EmailBroadcastRepository, users model.UserRepository, sender Mailer, opts Options) *Sender {
	s := &Sender{
		broadcasts: broadcasts,
		users:      users,
		mailer:     sender,
		interval:   defaultInterval,
		batchSize:  defaultBatchSize,
		batchPause: defaultBatchPause,
		running:    make(map[uint64]struct{}),
	}
	if opts.Interval > 0 {
		s.interval = opts.Interval
	}
	if opts.BatchSize > 0 {
		s.batchSize = opts.BatchSize
	}
	if opts.BatchPause > 0 {
		s.batchPause = opts.BatchPause
	}
	return s
}

// Enqueue 按 query 枚举用户并写入收件人名单，返回实际入队人数。
//
// 过滤规则（三者的共同目的是"别把邮件发给不该发或发不到的人"）：
//   - 邮箱为空 → 跳过（用户表允许邮箱为空，历史账号与管理員建的号常常没有邮箱）；
//   - 同一邮箱出现多次 → 只留第一条（同一邮箱收两封完全相同的通知纯属骚扰）；
//   - 已在名单里的用户 → 由仓储的 INSERT OR IGNORE 跳过（重复调用安全）。
func (s *Sender) Enqueue(ctx context.Context, bc *model.EmailBroadcast, query model.UserQuery) (int, error) {
	if bc == nil || bc.ID == 0 {
		return 0, model.ErrEmailBroadcastNotFound
	}

	seen := make(map[string]struct{})
	total := 0
	offset := 0
	for {
		page := query
		page.Limit = enqueuePageSize
		page.Offset = offset

		users, err := s.users.List(ctx, page)
		if err != nil {
			return total, err
		}
		if len(users) == 0 {
			break
		}

		batch := make([]*model.EmailBroadcastRecipient, 0, len(users))
		for _, user := range users {
			if user == nil {
				continue
			}
			email := model.NormalizeEmail(user.Email)
			if email == "" {
				continue
			}
			if _, dup := seen[email]; dup {
				continue
			}
			seen[email] = struct{}{}
			batch = append(batch, &model.EmailBroadcastRecipient{UserID: user.ID, Email: email})
		}

		if len(batch) > 0 {
			added, err := s.broadcasts.AddRecipients(ctx, bc.ID, batch)
			if err != nil {
				return total, err
			}
			total += added
		}

		if len(users) < enqueuePageSize {
			break
		}
		offset += len(users)
	}
	return total, nil
}

// Start 在后台开始发送该批次；返回 false 表示该批次已在发送中（重复触发被忽略）。
func (s *Sender) Start(bcID uint64) bool {
	if !s.acquire(bcID) {
		return false
	}
	go func() {
		defer s.release(bcID)
		s.runByID(bcID)
	}()
	return true
}

// ResumeAll 在进程启动时续发所有未完成的批次。
//
// 刻意串行（一个后台 goroutine 依次处理）：重启后若有多个未完成任务，
// 并行发送会同时打开多条 SMTP 连接，既难过服务商限流，也让"谁在发"难以排查。
func (s *Sender) ResumeAll(ctx context.Context) {
	go func() {
		items, err := s.broadcasts.ListUnfinished(ctx)
		if err != nil {
			slog.Error("恢复未完成的邮件群发失败", "error", err)
			return
		}
		if len(items) == 0 {
			return
		}
		slog.Info("发现未完成的邮件群发，开始续发", "count", len(items))
		for _, bc := range items {
			if !s.acquire(bc.ID) {
				continue
			}
			s.runByID(bc.ID)
			s.release(bc.ID)
		}
	}()
}

// runByID 读批次并执行，错误只记录不抛出（后台 goroutine 无调用方接错）。
func (s *Sender) runByID(bcID uint64) {
	// 用 Background 而不是某个请求的 context：发送由后台接口触发，
	// 请求早已返回，跟着请求 context 结束会把发送打断在半途。
	ctx := context.Background()

	bc, err := s.broadcasts.GetByID(ctx, bcID)
	if err != nil {
		slog.Error("读取待发批次失败", "broadcast_id", bcID, "error", err)
		return
	}
	if bc.IsFinished() {
		return
	}
	if err := s.Run(ctx, bc); err != nil {
		slog.Error("邮件群发中断", "broadcast_id", bcID, "error", err)
	}
}

// Run 同步执行一个批次的发送，直到发完、被停止或出错。
//
// 可独立调用（不经 Start）以便测试逐封校验行为。
func (s *Sender) Run(ctx context.Context, bc *model.EmailBroadcast) error {
	if bc == nil || bc.ID == 0 {
		return model.ErrEmailBroadcastNotFound
	}

	// 先确认批次仍处于"可发送"状态。
	//
	// 这一步不能省：下面会把状态写成 running，如果入口不先看一眼，
	// 一个已被管理员【明确停止】的批次只要被误调用一次就会被重新启动并继续群发——
	// 等于把"停止"按钮变成装饰（这是用单测抓出来的真实缺陷）。
	current, err := s.broadcasts.GetByID(ctx, bc.ID)
	if err != nil {
		return err
	}
	if current.Status == model.BroadcastStatusCanceled || current.Status == model.BroadcastStatusDone {
		return nil
	}

	// 重算一次进度：进程可能在上一轮中途退出，起始计数必须来自明细表而非内存。
	if err := s.syncProgress(ctx, bc.ID, model.BroadcastStatusRunning, time.Now(), time.Time{}); err != nil {
		return err
	}

	sentInBatch := 0
	for {
		// 每一轮开始前确认批次是否被停止：停止按钮要在一封之内生效，
		// 不能等整批（50 封）发完才响应 —— 那意味着多发了上百封错误邮件。
		roundState, err := s.broadcasts.GetByID(ctx, bc.ID)
		if err != nil {
			return err
		}
		if roundState.Status == model.BroadcastStatusCanceled {
			slog.Info("邮件群发已被停止", "broadcast_id", bc.ID)
			_ = s.syncProgress(ctx, bc.ID, model.BroadcastStatusCanceled, time.Time{}, time.Now())
			return nil
		}

		items, err := s.broadcasts.NextPending(ctx, bc.ID, s.batchSize)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			break
		}

		for _, item := range items {
			// 服务进程正在退出时不再继续发：未发的部分留给下次启动续发（状态仍是 pending）。
			if err := ctx.Err(); err != nil {
				return err
			}

			// 每封之前再确认一次状态：把"停止"的响应延迟压到一封以内。
			fresh, err := s.broadcasts.GetByID(ctx, bc.ID)
			if err != nil {
				return err
			}
			if fresh.Status == model.BroadcastStatusCanceled {
				_ = s.syncProgress(ctx, bc.ID, model.BroadcastStatusCanceled, time.Time{}, time.Now())
				return nil
			}

			s.sendOne(ctx, item, bc)
			sentInBatch++

			if !sleepCtx(ctx, s.interval) {
				_ = s.syncProgress(ctx, bc.ID, model.BroadcastStatusRunning, time.Time{}, time.Time{})
				return ctx.Err()
			}
		}

		// 一批结束：落库一次进度，让后台页面能看到推进。
		if err := s.syncProgress(ctx, bc.ID, model.BroadcastStatusRunning, time.Time{}, time.Time{}); err != nil {
			return err
		}

		// 批间停顿；若已无待发则不必等。
		remaining, err := s.broadcasts.NextPending(ctx, bc.ID, 1)
		if err != nil {
			return err
		}
		if len(remaining) == 0 {
			break
		}
		if sentInBatch >= s.batchSize {
			sentInBatch = 0
			if !sleepCtx(ctx, s.batchPause) {
				break
			}
		}
	}

	return s.syncProgress(ctx, bc.ID, model.BroadcastStatusDone, time.Time{}, time.Now())
}

// sendOne 发送单封并把结果落到该收件人身上。
//
// 失败【不重试】：邮件服务商的失败多为"地址不存在/被拒收"，反复重试无意义，
// 还会恶化发信声誉；失败原因落库，需要时由站长看明细人工决定。
func (s *Sender) sendOne(ctx context.Context, item *model.EmailBroadcastRecipient, bc *model.EmailBroadcast) {
	now := time.Now()
	if err := s.mailer.Send(ctx, item.Email, bc.Subject, bc.BodyHTML); err != nil {
		slog.Warn("群发单封失败", "broadcast_id", bc.ID, "user_id", item.UserID, "error", err)
		if err := s.broadcasts.MarkRecipient(ctx, item.ID, model.RecipientStatusFailed, err.Error(), now); err != nil {
			slog.Error("记录群发失败状态出错", "recipient_id", item.ID, "error", err)
		}
		return
	}
	if err := s.broadcasts.MarkRecipient(ctx, item.ID, model.RecipientStatusSent, "", now); err != nil {
		slog.Error("记录群发成功状态出错", "recipient_id", item.ID, "error", err)
	}
}

// syncProgress 从收件人明细重算进度并落库（计数是派生值，不靠内存累加）。
func (s *Sender) syncProgress(ctx context.Context, bcID uint64, status model.BroadcastStatus,
	startedAt, finishedAt time.Time) error {
	total, err := s.broadcasts.CountRecipients(ctx, bcID, "")
	if err != nil {
		return err
	}
	sent, err := s.broadcasts.CountRecipients(ctx, bcID, model.RecipientStatusSent)
	if err != nil {
		return err
	}
	failed, err := s.broadcasts.CountRecipients(ctx, bcID, model.RecipientStatusFailed)
	if err != nil {
		return err
	}
	return s.broadcasts.UpdateProgress(ctx, bcID, status, total, sent, failed, startedAt, finishedAt)
}

// acquire 尝试占用某批次的发送权，返回是否成功。
func (s *Sender) acquire(bcID uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[bcID]; ok {
		return false
	}
	s.running[bcID] = struct{}{}
	return true
}

// release 释放发送权。
func (s *Sender) release(bcID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, bcID)
}

// sleepCtx 可被取消的等待；返回 false 表示等待期间被取消。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
