// 本文件是 model.UserRepository 与 model.SessionRepository 的 SQL 实现。
//
// 意图（Why）：
//
//	把用户与会话落到数据库。此层承担两项职责：
//	  1) 把数据库错误翻译成领域错误（重复用户名 → ErrUsernameTaken；
//	     重复邮箱 → ErrEmailTaken；查不到 → ErrUserNotFound / ErrSessionNotFound），
//	     让上层无需识别驱动错误；
//	  2) 保证口令哈希与会话摘要"只进不出"——本层不返回明文口令，
//	     会话查询也只按摘要匹配，不对摘要做任何反解。
//
// 流转（Flow）：
//
//	NewUserRepository(db) / NewSessionRepository(db)
//	  └─ 认证处理器：注册（Create）、登录（GetByUsername + Create Session）、
//	     鉴权（GetByTokenHash → GetByID）、退出（DeleteByTokenHash）
//
// 扩展（Extend）：
//
//	新增用户字段：先建迁移脚本加列，再同步更新本文件的 userColumns / insert / update / scanUser 四处。
//	例外——登录安全类字段（failed_logins / locked_until / last_login_at / last_login_ip）
//	刻意【只进 userColumns/scanUser，不进 insert/update】：
//	  1) 新号默认 0，由迁移的 DEFAULT 兜底，insert 无需带；
//	  2) 它们由登录流水单列更新（RecordLoginSuccess / RegisterLoginFailure），
//	     若混进 Update 的整行写回，登录路径上读到的陈旧副本会把并发期间的计数清掉，
//	     等于给撞库开了一条"登录一次就能重置别人失败计数"的口子。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitee.com/xiaosu4610/aqua-api/internal/model"
)

// 用户列表查询的条数约束。
const (
	defaultUserListLimit = 20
	maxUserListLimit     = 100
)

// userColumns 集中定义查询列，顺序必须与 scanUser 的扫描顺序严格一致。
const userColumns = `id, username, password_hash, email, role, status, quota, used_quota, invite_code, inviter_id, agent_group, failed_logins, locked_until, last_login_at, last_login_ip, created_at, updated_at`

// maxInviteCodeAttempts 是注册时生成邀请码的最大重试次数。
//
// 邀请码是 8 位 32 字符集（约 40 bit 熵），撞码概率极低；
// 设一个小的上限只是为了让"极端异常"（索引损坏、随机源异常）不会变成死循环。
const maxInviteCodeAttempts = 5

// userRepository 是 model.UserRepository 的 SQL 实现，并发安全。
type userRepository struct {
	db *sql.DB
}

// NewUserRepository 创建用户仓储。
func NewUserRepository(db *sql.DB) model.UserRepository {
	return &userRepository{db: db}
}

// Create 新增用户。
//
// 邀请码在【注册时生成】（若未显式指定）：让每个用户从建号起就拥有邀请凭证，
// 无需等到首次访问邀请页再补。生成与插入放在同一重试循环里，因为唯一索引
// （users.invite_code 部分索引）才是并发下"邀请码唯一"的唯一可靠保证——
// 撞码（概率极低）时换一个重试即可，而用户名冲突必须立即返回而不是重试。
func (r *userRepository) Create(ctx context.Context, u *model.User) error {
	if err := u.Validate(); err != nil {
		return fmt.Errorf("store: 用户数据非法: %w", err)
	}

	now := time.Now()
	u.CreatedAt = now
	u.UpdatedAt = now

	for attempt := 0; ; attempt++ {
		if u.InviteCode == "" {
			code, err := model.GenerateInviteCode()
			if err != nil {
				return fmt.Errorf("store: 生成邀请码失败: %w", err)
			}
			u.InviteCode = code
		}

		res, err := r.db.ExecContext(ctx, `
			INSERT INTO users (username, password_hash, email, role, status, quota, used_quota, invite_code, inviter_id, agent_group, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.Username, u.PasswordHash, u.Email, int(u.Role), int(u.Status),
			u.Quota, u.UsedQuota, u.InviteCode, u.InviterID, u.AgentGroup, u.CreatedAt.Unix(), u.UpdatedAt.Unix(),
		)
		if err != nil {
			// 唯一索引冲突有三种来源，必须区分：用户名与邮箱重复是【业务错误】，直接上抛；
			// 邀请码撞码是【极小概率的随机冲突】，清空后重新生成再试一次。
			//
			// 判定依据是错误文本里的索引列名（SQLite 形如
			// "UNIQUE constraint failed: users.email"），因此必须逐列精确匹配，
			// 不能"见到 UNIQUE 就当用户名冲突"——那会把邮箱冲突误报成用户名冲突。
			if upper := strings.ToUpper(err.Error()); strings.Contains(upper, "UNIQUE") {
				switch {
				case strings.Contains(upper, "INVITE_CODE"):
					u.InviteCode = ""
					if attempt+1 >= maxInviteCodeAttempts {
						return fmt.Errorf("store: 连续 %d 次生成到重复邀请码，请重试: %w", maxInviteCodeAttempts, err)
					}
					continue
				case strings.Contains(upper, "EMAIL"):
					return model.ErrEmailTaken
				default:
					return model.ErrUsernameTaken
				}
			}
			return fmt.Errorf("store: 新增用户失败: %w", err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: 读取新增用户的 ID 失败: %w", err)
		}
		u.ID = uint64(id)
		return nil
	}
}

// GetByID 按主键查询用户。
func (r *userRepository) GetByID(ctx context.Context, id uint64) (*model.User, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id)

	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrUserNotFound
		}
		return nil, err
	}
	return u, nil
}

// GetByUsername 按登录名查询用户。
func (r *userRepository) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE username = ?", username)

	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrUserNotFound
		}
		return nil, err
	}
	return u, nil
}

// GetByEmail 按邮箱查询用户。
//
// 邮箱在库中已归一化（见迁移 0036：先 TRIM+LOWER 再建部分唯一索引），
// 这里仍再归一化一次，让调用方无需关心"该传原文还是小写"。
// 空邮箱直接返回 ErrUserNotFound：未绑定邮箱的用户在库中大量共存，
// 按空串查询只会随机命中其中一个，没有任何业务含义。
func (r *userRepository) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	email = model.NormalizeEmail(email)
	if email == "" {
		return nil, model.ErrUserNotFound
	}

	row := r.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE email = ?", email)

	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrUserNotFound
		}
		return nil, err
	}
	return u, nil
}

// List 按条件查询用户列表。
func (r *userRepository) List(ctx context.Context, q model.UserQuery) ([]*model.User, error) {
	where, args := buildUserWhere(q)

	var sb strings.Builder
	sb.WriteString("SELECT " + userColumns + " FROM users")
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}
	sb.WriteString(" ORDER BY id ASC")

	limit := normalizeLimit(q.Limit, defaultUserListLimit, maxUserListLimit)
	offset := normalizeOffset(q.Offset)
	sb.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询用户列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	users := make([]*model.User, 0, limit)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历用户结果集失败: %w", err)
	}
	return users, nil
}

// Count 返回符合条件的用户总数。
func (r *userRepository) Count(ctx context.Context, q model.UserQuery) (int, error) {
	where, args := buildUserWhere(q)

	sb := strings.Builder{}
	sb.WriteString("SELECT COUNT(1) FROM users")
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, sb.String(), args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("store: 统计用户数失败: %w", err)
	}
	return total, nil
}

// Update 按 ID 更新用户。
//
// 刻意不更新 invite_code 与 inviter_id：它们是"身份/关系"字段，不属于资料编辑。
// 若在此处一并写回，用户编辑资料用的陈旧副本会把刚生成的邀请码或刚建立的邀请关系覆盖掉。
// 这两列的变更只允许经由专门的路径（Create 生成、BindInviter 建立、EnsureInviteCode 懒生成）。
func (r *userRepository) Update(ctx context.Context, u *model.User) error {
	if u.ID == 0 {
		return errors.New("store: 更新用户时 ID 不能为 0")
	}
	if err := u.Validate(); err != nil {
		return fmt.Errorf("store: 用户数据非法: %w", err)
	}

	u.UpdatedAt = time.Now()

	res, err := r.db.ExecContext(ctx, `
		UPDATE users SET
			username = ?, password_hash = ?, email = ?, role = ?, status = ?,
			quota = ?, used_quota = ?, agent_group = ?, updated_at = ?
		WHERE id = ?`,
		u.Username, u.PasswordHash, u.Email, int(u.Role), int(u.Status),
		u.Quota, u.UsedQuota, u.AgentGroup, u.UpdatedAt.Unix(), u.ID,
	)
	if err != nil {
		// 唯一索引冲突须区分用户名与邮箱：两者对使用者的可操作性不同
		// （改名字 vs. 该邮箱已注册），不能一律报成用户名冲突。
		if upper := strings.ToUpper(err.Error()); strings.Contains(upper, "UNIQUE") {
			if strings.Contains(upper, "EMAIL") {
				return model.ErrEmailTaken
			}
			return model.ErrUsernameTaken
		}
		return fmt.Errorf("store: 更新用户 %d 失败: %w", u.ID, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取更新影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrUserNotFound
	}
	return nil
}

// UpdatePassword 按 ID 只更新口令哈希（改密/重置密码专用）。
//
// 刻意不复用 Update：Update 会把整行写回，调用方若拿的是改密前读到的副本，
// 就会把并发期间刚入账的额度或刚改的状态覆盖回去。
func (r *userRepository) UpdatePassword(ctx context.Context, id uint64, passwordHash string) error {
	hash := strings.TrimSpace(passwordHash)
	if hash == "" {
		return errors.New("store: 口令哈希不能为空")
	}

	res, err := r.db.ExecContext(ctx,
		"UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		hash, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("store: 更新用户 %d 口令失败: %w", id, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取更新影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrUserNotFound
	}
	return nil
}

// RecordLoginSuccess 记录一次成功登录：清零失败计数与锁定，写入来源。
//
// 为什么是"一条 UPDATE 打四个单列"而不是复用 Update：
// 登录路径上持有的是本次请求开始时读到的副本，整行写回会覆盖并发期间的额度变化；
// 而且登录不应刷新 updated_at（那是"资料修改时间"）。
func (r *userRepository) RecordLoginSuccess(ctx context.Context, id uint64, ip string, at time.Time) error {
	if ip == "" {
		ip = "unknown"
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE users SET failed_logins = 0, locked_until = 0,
			last_login_at = ?, last_login_ip = ?
		WHERE id = ?`,
		at.Unix(), ip, id,
	)
	if err != nil {
		return fmt.Errorf("store: 记录登录成功失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取登录记录影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrUserNotFound
	}
	return nil
}

// RegisterLoginFailure 登记一次登录失败：计数 +1，达到阈值时置锁定截止。
//
// 计数与锁定判定在同一条 UPDATE 内完成（`failed_logins + 1` 先算再比），
// 因此并发失败不会数丢、也不会出现"两边都以为对方已锁定"的漏锁。
// 阈值与锁期取自 model 常量，SQL 侧不重复定义，避免两处口径漂移。
func (r *userRepository) RegisterLoginFailure(ctx context.Context, id uint64, now time.Time) (int, time.Time, error) {
	lockUntil := now.Add(model.LoginFailureLockDuration).Unix()
	if _, err := r.db.ExecContext(ctx, `
		UPDATE users SET
			failed_logins = failed_logins + 1,
			locked_until = CASE WHEN failed_logins + 1 >= ? THEN ? ELSE locked_until END
		WHERE id = ?`,
		model.LoginFailureLockThreshold, lockUntil, id,
	); err != nil {
		return 0, time.Time{}, fmt.Errorf("store: 记录登录失败: %w", err)
	}

	var failed int
	var lockedUnix int64
	row := r.db.QueryRowContext(ctx,
		"SELECT failed_logins, locked_until FROM users WHERE id = ?", id)
	if err := row.Scan(&failed, &lockedUnix); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, time.Time{}, model.ErrUserNotFound
		}
		return 0, time.Time{}, fmt.Errorf("store: 读取登录失败计数: %w", err)
	}
	return failed, unixToTime(lockedUnix), nil
}

// Delete 按 ID 删除用户。
func (r *userRepository) Delete(ctx context.Context, id uint64) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: 删除用户 %d 失败: %w", id, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取删除影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrUserNotFound
	}
	return nil
}

// AddUsedQuota 增量累加已用额度。
//
// 实现要点：用 SQL 的原子自增（used_quota = used_quota + ?）而不是
// "读出-计算-写回"，后者在并发请求下会互相覆盖，导致用量统计偏小。
func (r *userRepository) AddUsedQuota(ctx context.Context, id uint64, delta int64) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE users SET used_quota = used_quota + ?, updated_at = ? WHERE id = ?",
		delta, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("store: 更新用户 %d 已用额度失败: %w", id, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrUserNotFound
	}
	return nil
}

// AddQuota 累加用户总额度（充值入账），并返回累加后的总额度。
//
// 实现要点：
//  1. 单条 SQL 自增，避免"读-改-写"在并发入账时互相覆盖；
//  2. quota = -1 表示不限额度，此时不做任何修改——
//     给"不限"加数字会把它变成有限额度，属于最不该发生的资损。
//
// 返回值语义：返回累加后的总额度；不限额度时返回 QuotaUnlimited。
func (r *userRepository) AddQuota(ctx context.Context, id uint64, delta int64) (int64, error) {
	if _, err := r.db.ExecContext(ctx, `
		UPDATE users SET
			quota = quota + ?,
			updated_at = ?
		WHERE id = ? AND quota != ?`,
		delta, time.Now().Unix(), id, model.QuotaUnlimited); err != nil {
		return 0, fmt.Errorf("store: 累加用户 %d 额度失败: %w", id, err)
	}

	var quota int64
	if err := r.db.QueryRowContext(ctx, "SELECT quota FROM users WHERE id = ?", id).Scan(&quota); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, model.ErrUserNotFound
		}
		return 0, fmt.Errorf("store: 读取用户 %d 额度失败: %w", id, err)
	}
	return quota, nil
}

// CountAdmins 返回管理员数量。
func (r *userRepository) CountAdmins(ctx context.Context) (int, error) {
	var total int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM users WHERE role = ?", int(model.UserRoleAdmin)).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("store: 统计管理员数失败: %w", err)
	}
	return total, nil
}

// maxAdminLookupLimit 是枚举管理员的硬上限。
//
// 取 5：超管入口要逐个比对 bcrypt 哈希，而未鉴权的密码比对是典型的 CPU 放大面。
// 正常部署只有 1~2 个管理员，超过 5 个时说明站点把管理员当普通用户在用，
// 此时应当改用"用户名 + 密码"登录（见 handler_install 的说明），而不是让
// 一个未鉴权接口去做 N 次昂贵的哈希运算。
const maxAdminLookupLimit = 5

// ListAdmins 返回最多 limit 个管理员，按 ID 升序。
//
// limit 会被夹到 [1, maxAdminLookupLimit]，调用方无法通过传大数值绕过限制。
func (r *userRepository) ListAdmins(ctx context.Context, limit int) ([]*model.User, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > maxAdminLookupLimit {
		limit = maxAdminLookupLimit
	}

	rows, err := r.db.QueryContext(ctx,
		"SELECT "+userColumns+" FROM users WHERE role = ? ORDER BY id ASC LIMIT ?",
		int(model.UserRoleAdmin), limit)
	if err != nil {
		return nil, fmt.Errorf("store: 查询管理员列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	admins := make([]*model.User, 0, limit)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		admins = append(admins, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历管理员结果集失败: %w", err)
	}
	return admins, nil
}

// buildUserWhere 构造用户查询的 WHERE 子句与参数（全部使用占位符，杜绝 SQL 注入）。
func buildUserWhere(q model.UserQuery) (string, []any) {
	var (
		conditions []string
		args       []any
	)

	if keyword := strings.TrimSpace(q.Keyword); keyword != "" {
		// 模糊匹配用户名或邮箱；转义 % 与 _ 避免用户输入被当作通配符
		pattern := "%" + escapeLike(keyword) + "%"
		conditions = append(conditions, "(username LIKE ? ESCAPE '\\' OR email LIKE ? ESCAPE '\\')")
		args = append(args, pattern, pattern)
	}
	if q.Role != nil {
		conditions = append(conditions, "role = ?")
		args = append(args, int(*q.Role))
	}
	if q.Status != nil {
		conditions = append(conditions, "status = ?")
		args = append(args, int(*q.Status))
	}

	return strings.Join(conditions, " AND "), args
}

// scanUser 把一行数据映射为用户对象。
//
// 参数为 rowScanner 接口，从而同时支持 *sql.Row 与 *sql.Rows。
func scanUser(sc rowScanner) (*model.User, error) {
	var (
		id           uint64
		username     string
		passwordHash string
		email        string
		role         int
		status       int
		quota        int64
		usedQuota    int64
		inviteCode   string
		inviterID    uint64
		agentGroup   string
		failedLogins int
		lockedUntil  int64
		lastLoginAt  int64
		lastLoginIP  string
		createdAt    int64
		updatedAt    int64
	)

	if err := sc.Scan(&id, &username, &passwordHash, &email, &role, &status,
		&quota, &usedQuota, &inviteCode, &inviterID, &agentGroup,
		&failedLogins, &lockedUntil, &lastLoginAt, &lastLoginIP,
		&createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取用户字段失败: %w", err)
	}

	return &model.User{
		ID:           id,
		Username:     username,
		PasswordHash: passwordHash,
		Email:        email,
		Role:         model.UserRole(role),
		Status:       model.UserStatus(status),
		Quota:        quota,
		UsedQuota:    usedQuota,
		InviteCode:   inviteCode,
		InviterID:    inviterID,
		AgentGroup:   agentGroup,
		FailedLogins: failedLogins,
		// 库里的 0 表示"从未锁定 / 从未登录"，必须映射成零值 time.Time：
		// 若直接 time.Unix(0,0) 会得到 1970 年，IsLocked 会把它当成"锁到 1970"之后的
		// 永远已过期（判断没错），但 IsZero 语义就丢了——异地提醒靠它识别"首次登录"。
		LockedUntil: unixToTime(lockedUntil),
		LastLoginAt: unixToTime(lastLoginAt),
		LastLoginIP: lastLoginIP,
		CreatedAt:   time.Unix(createdAt, 0),
		UpdatedAt:   time.Unix(updatedAt, 0),
	}, nil
}

// unixToTime 把 Unix 秒转为 time.Time，0（含负数）返回零值 time.Time。
//
// 与 time.Unix(0,0) 的区别：后者是 1970 年这一具体时刻，会让
// "从未发生过" 与 "很久以前发生过" 无法区分，而安全判定恰恰依赖前者。
func unixToTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// ---------------------------------------------------------------------------
// 会话仓储
// ---------------------------------------------------------------------------

// sessionColumns 定义会话查询列。
const sessionColumns = `id, user_id, token_hash, expires_at, created_at, ip, user_agent, reauth_at`

// sessionRepository 是 model.SessionRepository 的 SQL 实现。
type sessionRepository struct {
	db *sql.DB
}

// NewSessionRepository 创建会话仓储。
func NewSessionRepository(db *sql.DB) model.SessionRepository {
	return &sessionRepository{db: db}
}

// Create 新增会话。
func (r *sessionRepository) Create(ctx context.Context, s *model.Session) error {
	if s.UserID == 0 {
		return errors.New("store: 会话必须归属某个用户")
	}
	if s.TokenHash == "" {
		return errors.New("store: 会话令牌摘要不能为空")
	}
	if s.ExpiresAt.IsZero() {
		return errors.New("store: 会话必须设置过期时间（永久会话一旦泄露无法自动失效）")
	}

	s.CreatedAt = time.Now()
	// UA 完全由客户端控制，入库前截断，防止一个超长头把行撑大
	// （上限见 model.UserAgentMaxLength）。
	ua := s.UserAgent
	if len(ua) > model.UserAgentMaxLength {
		ua = ua[:model.UserAgentMaxLength]
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO sessions (user_id, token_hash, expires_at, created_at, ip, user_agent, reauth_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.UserID, s.TokenHash, s.ExpiresAt.Unix(), s.CreatedAt.Unix(),
		s.IP, ua, reauthUnix(s.ReauthAt),
	)
	if err != nil {
		return fmt.Errorf("store: 创建会话失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: 读取会话 ID 失败: %w", err)
	}
	s.ID = uint64(id)
	return nil
}

// GetByTokenHash 按令牌摘要查询会话。
func (r *sessionRepository) GetByTokenHash(ctx context.Context, tokenHash string) (*model.Session, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+sessionColumns+" FROM sessions WHERE token_hash = ?", tokenHash)

	s, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrSessionNotFound
		}
		return nil, err
	}
	return s, nil
}

// DeleteByTokenHash 按摘要删除会话（退出登录）。
func (r *sessionRepository) DeleteByTokenHash(ctx context.Context, tokenHash string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash); err != nil {
		return fmt.Errorf("store: 删除会话失败: %w", err)
	}
	// 不存在也视为成功：重复退出登录不应报错
	return nil
}

// DeleteByUserID 删除某用户的全部会话（用于禁用用户或改密后强制下线）。
func (r *sessionRepository) DeleteByUserID(ctx context.Context, userID uint64) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("store: 删除用户 %d 的会话失败: %w", userID, err)
	}
	return nil
}

// DeleteExpired 清理过期会话。
//
// 为什么需要：会话表会随登录次数持续增长，虽有 expires_at 判定但不清理会无限膨胀。
// 由启动时与定期任务调用（实现见 main 中的清理逻辑）。
func (r *sessionRepository) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", before.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: 清理过期会话失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取清理条数失败: %w", err)
	}
	return affected, nil
}

// UpdateReauth 记录该会话最近一次"重新验证密码"的时刻。
//
// 会话被同时删除（改密/禁用踢下线）时 affected 为 0，属正常并发，不报错——
// 会话都没了，验证结果自然无需保留。
func (r *sessionRepository) UpdateReauth(ctx context.Context, id uint64, at time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE sessions SET reauth_at = ? WHERE id = ?", at.Unix(), id); err != nil {
		return fmt.Errorf("store: 记录会话二次验证失败: %w", err)
	}
	return nil
}

// scanSession 把一行数据映射为会话对象。
func scanSession(sc rowScanner) (*model.Session, error) {
	var (
		id        uint64
		userID    uint64
		tokenHash string
		expiresAt int64
		createdAt int64
		ip        string
		userAgent string
		reauthAt  int64
	)

	if err := sc.Scan(&id, &userID, &tokenHash, &expiresAt, &createdAt,
		&ip, &userAgent, &reauthAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取会话字段失败: %w", err)
	}

	return &model.Session{
		ID:        id,
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: time.Unix(expiresAt, 0),
		CreatedAt: time.Unix(createdAt, 0),
		IP:        ip,
		UserAgent: userAgent,
		ReauthAt:  unixToTime(reauthAt),
	}, nil
}

// reauthUnix 把二次验证时刻转成入库值：零值时刻入 0（表示"从未验证"）。
func reauthUnix(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.Unix()
}

// ---------------------------------------------------------------------------
// 通用辅助（供本包各仓储复用）
// ---------------------------------------------------------------------------

// normalizeLimit 把分页条数归一化到 [1, max] 区间。
func normalizeLimit(limit, defaultLimit, maxLimit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// normalizeOffset 归一化分页偏移量。
func normalizeOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

// escapeLike 转义 LIKE 模式中的通配符。
//
// 必要性：用户搜索 "50%" 时，若不转义会变成"以 50 开头"的模糊匹配，
// 返回大量无关结果，甚至被用于构造恶意模式造成全表扫描。
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}
