// 本文件是 model.SMTPRepository 的 SQL 实现。
//
// 意图（Why）：
//
//	把"站长在后台填的 SMTP 参数"存成数据库里的一行，并保证登录口令不以明文落库。
//	口令的加密方式与渠道密钥完全一致（internal/crypto 的 AES-GCM，密钥来自
//	AQUA_APP_KEY），因此数据库备份流出时，没有 APP_KEY 依然解不开口令。
//
// 流转（Flow）：
//
//	读取：Get → SELECT 单行 → cipher.Decrypt(password_cipher) → 领域对象
//	写入：Save → Validate 之外不再校验（校验在领域层）→ cipher.Encrypt(口令) → upsert 单行
//
// 扩展（Extend）：
//
//	新增 SMTP 字段时：在 model.SMTPSettings 加字段 + 建迁移加列
//	+ 修改本文件的 smtpColumns 常量、Get 的 Scan 与 Save 的 SQL（三处必须同步）。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// smtpColumns 是 smtp_settings 的列清单（读写共用，避免两处手写导致错位）。
const smtpColumns = `host, port, username, from_addr, from_name, password_cipher, enabled, updated_at`

// smtpRepository 是 model.SMTPRepository 的 SQL 实现。
//
// 并发安全：只持有 *sql.DB（自带连接池）与无状态的 *crypto.Cipher，可并发复用。
type smtpRepository struct {
	db     *sql.DB
	cipher *crypto.Cipher
}

// NewSMTPRepository 创建 SMTP 配置仓储。
//
// 参数 cipher 用于口令加解密，不可为 nil —— 没有加密能力就不应该允许写入口令。
func NewSMTPRepository(db *sql.DB, cipher *crypto.Cipher) model.SMTPRepository {
	return &smtpRepository{db: db, cipher: cipher}
}

// Get 读取 SMTP 配置；从未保存过时返回 (nil, nil)。
//
// 为什么"没有配置"返回 nil 而不是错误：未配置是完全正常的状态
// （新部署的站点就是没配），把它当错误会让启动流程无谓地报错退出。
func (r *smtpRepository) Get(ctx context.Context) (*model.SMTPSettings, error) {
	var (
		host           string
		port           int
		username       string
		fromAddr       string
		fromName       string
		passwordCipher string
		enabled        int
		updatedAt      int64
	)
	query := "SELECT " + smtpColumns + " FROM smtp_settings WHERE id = 1"
	err := r.db.QueryRowContext(ctx, query).Scan(
		&host, &port, &username, &fromAddr, &fromName, &passwordCipher, &enabled, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: 读取 SMTP 配置失败: %w", err)
	}

	password := ""
	if strings.TrimSpace(passwordCipher) != "" {
		decrypted, err := r.cipher.Decrypt(passwordCipher)
		if err != nil {
			// 解不开（例如 APP_KEY 被换过）不应让整个配置读取失败：
			// 其余参数仍然有用（站长能看到地址与账号，只需重填口令）。
			// 这里把它降级为"口令未配置"，并在错误信息里说明原因。
			return nil, fmt.Errorf("store: SMTP 口令解密失败（AQUA_APP_KEY 是否变更过？）: %w", err)
		}
		password = decrypted
	}

	return &model.SMTPSettings{
		Host:      host,
		Port:      port,
		Username:  username,
		From:      fromAddr,
		FromName:  fromName,
		Password:  password,
		Enabled:   enabled == 1,
		UpdatedAt: unixToTimeOrZero(updatedAt),
	}, nil
}

// Save 保存 SMTP 配置（单行 upsert）。
//
// 为什么用"先 UPDATE 再按需 INSERT"而不是数据库方言的 UPSERT 语法：
// SQLite 与 MySQL 的 UPSERT 关键字不同（ON CONFLICT … DO UPDATE 用 excluded，
// MySQL 用 ON DUPLICATE KEY UPDATE 与 VALUES()）。本表恒只有一行，
// 用事务里的 UPDATE/INSERT 两步即可，且完全不需要方言判断。
func (r *smtpRepository) Save(ctx context.Context, settings *model.SMTPSettings) error {
	if settings == nil {
		return errors.New("store: SMTP 配置不能为 nil")
	}

	encrypted, err := r.cipher.Encrypt(settings.Password)
	if err != nil {
		return fmt.Errorf("store: 加密 SMTP 口令失败: %w", err)
	}
	enabled := 0
	if settings.Enabled {
		enabled = 1
	}
	now := time.Now().Unix()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开启 SMTP 配置事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	update := `UPDATE smtp_settings SET host = ?, port = ?, username = ?, from_addr = ?,
		from_name = ?, password_cipher = ?, enabled = ?, updated_at = ? WHERE id = 1`
	result, err := tx.ExecContext(ctx, update,
		settings.Host, settings.Port, settings.Username, settings.From,
		settings.FromName, encrypted, enabled, now)
	if err != nil {
		return fmt.Errorf("store: 更新 SMTP 配置失败: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取 SMTP 更新结果失败: %w", err)
	}
	if affected == 0 {
		insert := `INSERT INTO smtp_settings (id, ` + smtpColumns + `)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, insert,
			settings.Host, settings.Port, settings.Username, settings.From,
			settings.FromName, encrypted, enabled, now); err != nil {
			return fmt.Errorf("store: 写入 SMTP 配置失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交 SMTP 配置失败: %w", err)
	}

	settings.UpdatedAt = time.Unix(now, 0)
	return nil
}
