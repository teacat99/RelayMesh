package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/model"
)

// JWTSecretKey 是自动生成的 JWT 签名密钥在 system_settings 表中的键。
//
// 安全约束：该键不属于任何对外接口返回的设置集合（GetSettings 只读取 app_settings，标题类查询按 key 前缀过滤），
// 也不得被列出、导出或写入日志。新增「枚举全部 system_settings」的代码前必须先排除该键。
const JWTSecretKey = "jwt_secret"

// JWTSecretSource 标记当前生效的 JWT 签名密钥来源，仅用于日志与诊断。
type JWTSecretSource string

const (
	JWTSecretSourceEnv       JWTSecretSource = "env"       // 环境变量 RELAYMESH_JWT_SECRET（强度合格）
	JWTSecretSourceDB        JWTSecretSource = "database"  // 此前自动生成并持久化在数据库中的密钥
	JWTSecretSourceGenerated JWTSecretSource = "generated" // 本次启动新生成并已持久化
	JWTSecretSourceEphemeral JWTSecretSource = "ephemeral" // 持久化失败，仅本进程内存有效，重启后失效
)

const (
	jwtSecretRandomBytes  = 48 // 384 bit，base64url 编码后 64 字符
	jwtSecretPersistTries = 3
)

// JWTSecretResolution 是 ResolveJWTSecret 的结果。
type JWTSecretResolution struct {
	// Secret 是最终用于签名的密钥，恒满足 config.IsStrongJWTSecret。
	Secret string
	Source JWTSecretSource
	// EnvRejectedReason 非空表示运维显式设置了 RELAYMESH_JWT_SECRET 但强度不足而被忽略（原因不含密钥内容）。
	// 变量缺省（等于内置默认值）不算显式设置，不会填充该字段。
	EnvRejectedReason string
	// PersistError 仅在 Source 为 ephemeral 时非空：说明无法持久化的原因。
	PersistError error
}

// ResolveJWTSecret 决定本次运行使用的 JWT 签名密钥，优先级：
//
//  1. 强度合格的环境变量（envSecret）；
//  2. 数据库中已持久化的强密钥；
//  3. 首次启动自动生成 384 bit 随机密钥并持久化。
//
// 无论如何都不会返回内置默认值或弱密钥：数据库不可写时降级为仅本进程有效的随机密钥（重启后需重新登录），
// 而不是回退到可被公开源码推导出的固定值。只有系统随机数源本身失败才会返回错误。
func (s *Store) ResolveJWTSecret(ctx context.Context, envSecret string) (JWTSecretResolution, error) {
	envSecret = strings.TrimSpace(envSecret)
	reason := config.JWTSecretWeakness(envSecret)
	if reason == "" {
		return JWTSecretResolution{Secret: envSecret, Source: JWTSecretSourceEnv}, nil
	}

	res := JWTSecretResolution{}
	if envSecret != "" && envSecret != config.DefaultJWTSecret {
		res.EnvRejectedReason = reason
	}

	var lastErr error
	for attempt := 1; attempt <= jwtSecretPersistTries; attempt++ {
		secret, source, err := s.loadOrCreateJWTSecret(ctx)
		if err == nil {
			res.Secret, res.Source = secret, source
			return res, nil
		}
		lastErr = err
		if attempt == jwtSecretPersistTries || ctx.Err() != nil {
			break
		}
		// 多个进程同时首次启动并共用同一数据库文件时，后写入者可能撞上 SQLITE_BUSY_SNAPSHOT，稍等后重读即可拿到先写入者的密钥。
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
		}
	}

	secret, err := newRandomJWTSecret()
	if err != nil {
		return res, err
	}
	res.Secret, res.Source, res.PersistError = secret, JWTSecretSourceEphemeral, lastErr
	return res, nil
}

// loadOrCreateJWTSecret 在同一事务内完成「读取 → 校验 → 必要时生成并写入」。
func (s *Store) loadOrCreateJWTSecret(ctx context.Context) (string, JWTSecretSource, error) {
	var (
		secret string
		source JWTSecretSource
	)
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var record model.SystemSetting
		err := tx.Where("key = ?", JWTSecretKey).First(&record).Error
		switch {
		case err == nil:
			if config.IsStrongJWTSecret(record.Value) {
				secret, source = record.Value, JWTSecretSourceDB
				return nil
			}
			// 记录存在但强度不合格（被人工改写或损坏）：不采用，重新生成并覆盖。
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return err
		}

		generated, err := newRandomJWTSecret()
		if err != nil {
			return err
		}
		if err := tx.Save(&model.SystemSetting{Key: JWTSecretKey, Value: generated, UpdatedAt: time.Now()}).Error; err != nil {
			return err
		}
		secret, source = generated, JWTSecretSourceGenerated
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return secret, source, nil
}

// newRandomJWTSecret 从系统安全随机数源生成 base64url 编码的随机密钥，并保证其通过强度校验。
func newRandomJWTSecret() (string, error) {
	buf := make([]byte, jwtSecretRandomBytes)
	for i := 0; i < 8; i++ {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("读取系统随机数失败: %w", err)
		}
		secret := base64.RawURLEncoding.EncodeToString(buf)
		if config.IsStrongJWTSecret(secret) {
			return secret, nil
		}
	}
	return "", errors.New("生成的 JWT 密钥连续多次未通过强度校验")
}
