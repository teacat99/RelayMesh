package main

import (
	"context"
	"log"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/store"
)

// applyJWTSecret 解析本次运行使用的 JWT 签名密钥并写回 cfg.JWTSecret，必须在 api.NewServer 之前调用。
//
// 优先级：强度合格的环境变量 > 数据库中已持久化的密钥 > 首次启动自动生成并持久化。
// 内置默认值与弱密钥永远不会用于签名。日志只记录来源与原因，不输出密钥内容。
func applyJWTSecret(cfg *config.Config, st *store.Store) error {
	res, err := st.ResolveJWTSecret(context.Background(), cfg.JWTSecret)
	if err != nil {
		return err
	}
	cfg.JWTSecret = res.Secret

	if res.EnvRejectedReason != "" {
		log.Printf("[security] 警告: 环境变量 RELAYMESH_JWT_SECRET 已被忽略（%s），请改用不少于 %d 字符的随机字符串，或留空由系统自动生成",
			res.EnvRejectedReason, config.MinJWTSecretLength)
	}

	switch res.Source {
	case store.JWTSecretSourceEnv:
		log.Printf("[security] JWT 签名密钥来源: 环境变量 RELAYMESH_JWT_SECRET")
	case store.JWTSecretSourceDB:
		log.Printf("[security] JWT 签名密钥来源: 数据库（此前自动生成）")
	case store.JWTSecretSourceGenerated:
		log.Printf("[security] JWT 签名密钥: 已自动生成随机密钥并持久化到数据库（此前用旧密钥签发的登录令牌将失效，需要重新登录）")
	case store.JWTSecretSourceEphemeral:
		log.Printf("[security] 警告: JWT 签名密钥无法持久化（%v），本次运行使用临时随机密钥，重启后需要重新登录", res.PersistError)
	}
	return nil
}
