package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

const testStrongEnvSecret = "Xk9#mP2vL7qR4tWz8Yb3NcF6hJd5GsA1-env-supplied-by-operator"

func openFileStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := New(path)
	if err != nil {
		t.Fatalf("New(%s) failed: %v", path, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func readPersistedJWTSecret(t *testing.T, st *Store) (string, bool) {
	t.Helper()
	var record model.SystemSetting
	err := st.db.Where("key = ?", JWTSecretKey).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false
	}
	if err != nil {
		t.Fatalf("读取持久化 JWT 密钥失败: %v", err)
	}
	return record.Value, true
}

func TestResolveJWTSecret_StrongEnvWinsAndIsNotPersisted(t *testing.T) {
	st := openFileStore(t, filepath.Join(t.TempDir(), "relaymesh.db"))

	res, err := st.ResolveJWTSecret(context.Background(), "  "+testStrongEnvSecret+"  ")
	if err != nil {
		t.Fatalf("ResolveJWTSecret 失败: %v", err)
	}
	if res.Source != JWTSecretSourceEnv || res.Secret != testStrongEnvSecret {
		t.Fatalf("应采用环境变量（已去空白），实际 source=%s", res.Source)
	}
	if res.EnvRejectedReason != "" {
		t.Fatalf("合格的环境变量不应带有拒绝原因: %q", res.EnvRejectedReason)
	}
	if _, found := readPersistedJWTSecret(t, st); found {
		t.Fatalf("使用环境变量时不应向数据库写入密钥")
	}
}

func TestResolveJWTSecret_GeneratesPersistsAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relaymesh.db")
	st := openFileStore(t, path)

	first, err := st.ResolveJWTSecret(context.Background(), config.DefaultJWTSecret)
	if err != nil {
		t.Fatalf("首次解析失败: %v", err)
	}
	if first.Source != JWTSecretSourceGenerated {
		t.Fatalf("首次应为 generated，实际 %s", first.Source)
	}
	if !config.IsStrongJWTSecret(first.Secret) || first.Secret == config.DefaultJWTSecret {
		t.Fatalf("生成的密钥必须合格且不等于内置默认值")
	}
	if first.EnvRejectedReason != "" {
		t.Fatalf("环境变量缺省（哨兵默认值）不应被当成显式配置而告警: %q", first.EnvRejectedReason)
	}
	if persisted, found := readPersistedJWTSecret(t, st); !found || persisted != first.Secret {
		t.Fatalf("生成的密钥应已持久化")
	}

	second, err := st.ResolveJWTSecret(context.Background(), "")
	if err != nil {
		t.Fatalf("二次解析失败: %v", err)
	}
	if second.Source != JWTSecretSourceDB || second.Secret != first.Secret {
		t.Fatalf("二次应复用数据库密钥，实际 source=%s 是否相同=%v", second.Source, second.Secret == first.Secret)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	reopened := openFileStore(t, path)
	third, err := reopened.ResolveJWTSecret(context.Background(), config.DefaultJWTSecret)
	if err != nil {
		t.Fatalf("重启后解析失败: %v", err)
	}
	if third.Source != JWTSecretSourceDB || third.Secret != first.Secret {
		t.Fatalf("重启后应沿用同一密钥（保证登录态跨重启有效）")
	}
}

func TestResolveJWTSecret_ExplicitWeakEnvIsRejectedWithReason(t *testing.T) {
	st := openFileStore(t, filepath.Join(t.TempDir(), "relaymesh.db"))

	const weak = "short-secret"
	res, err := st.ResolveJWTSecret(context.Background(), weak)
	if err != nil {
		t.Fatalf("ResolveJWTSecret 失败: %v", err)
	}
	if res.EnvRejectedReason == "" {
		t.Fatalf("显式配置的弱密钥应给出拒绝原因")
	}
	if strings.Contains(res.EnvRejectedReason, weak) {
		t.Fatalf("拒绝原因不得包含密钥内容: %q", res.EnvRejectedReason)
	}
	if res.Secret == weak || !config.IsStrongJWTSecret(res.Secret) {
		t.Fatalf("弱环境变量不得用于签名")
	}
	if res.Source != JWTSecretSourceGenerated {
		t.Fatalf("应降级为自动生成，实际 %s", res.Source)
	}
}

func TestResolveJWTSecret_ReplacesWeakPersistedValue(t *testing.T) {
	st := openFileStore(t, filepath.Join(t.TempDir(), "relaymesh.db"))

	if err := st.db.Save(&model.SystemSetting{Key: JWTSecretKey, Value: "tampered", UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("写入弱密钥失败: %v", err)
	}
	res, err := st.ResolveJWTSecret(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveJWTSecret 失败: %v", err)
	}
	if res.Source != JWTSecretSourceGenerated || res.Secret == "tampered" {
		t.Fatalf("库中弱密钥应被丢弃并重新生成，实际 source=%s", res.Source)
	}
	if persisted, _ := readPersistedJWTSecret(t, st); persisted != res.Secret {
		t.Fatalf("新密钥应覆盖弱记录")
	}
}

func TestResolveJWTSecret_StrongEnvDoesNotTouchPersistedSecret(t *testing.T) {
	st := openFileStore(t, filepath.Join(t.TempDir(), "relaymesh.db"))

	generated, err := st.ResolveJWTSecret(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveJWTSecret 失败: %v", err)
	}
	viaEnv, err := st.ResolveJWTSecret(context.Background(), testStrongEnvSecret)
	if err != nil {
		t.Fatalf("ResolveJWTSecret 失败: %v", err)
	}
	if viaEnv.Source != JWTSecretSourceEnv {
		t.Fatalf("合格的环境变量应优先，实际 %s", viaEnv.Source)
	}
	if persisted, _ := readPersistedJWTSecret(t, st); persisted != generated.Secret {
		t.Fatalf("环境变量生效时不应改动已持久化的密钥（撤销环境变量后应能回到原密钥）")
	}
}

func TestResolveJWTSecret_DegradesToEphemeralWhenDatabaseUnavailable(t *testing.T) {
	st := openFileStore(t, filepath.Join(t.TempDir(), "relaymesh.db"))
	if err := st.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	first, err := st.ResolveJWTSecret(context.Background(), config.DefaultJWTSecret)
	if err != nil {
		t.Fatalf("数据库不可用时不应返回错误，而应降级: %v", err)
	}
	if first.Source != JWTSecretSourceEphemeral || first.PersistError == nil {
		t.Fatalf("应降级为 ephemeral 并携带持久化错误，实际 source=%s err=%v", first.Source, first.PersistError)
	}
	if !config.IsStrongJWTSecret(first.Secret) || first.Secret == config.DefaultJWTSecret {
		t.Fatalf("降级密钥同样必须合格且不得为内置默认值")
	}

	second, err := st.ResolveJWTSecret(context.Background(), config.DefaultJWTSecret)
	if err != nil {
		t.Fatalf("第二次解析失败: %v", err)
	}
	if second.Secret == first.Secret {
		t.Fatalf("临时密钥应逐次独立随机生成")
	}
}

func TestResolveJWTSecret_ConcurrentFirstStartConverges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relaymesh.db")
	stores := []*Store{openFileStore(t, path), openFileStore(t, path)}

	const perStore = 4
	results := make([]JWTSecretResolution, len(stores)*perStore)
	errs := make([]error, len(results))

	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = stores[i%len(stores)].ResolveJWTSecret(context.Background(), config.DefaultJWTSecret)
		}(i)
	}
	wg.Wait()

	generated := 0
	for i, res := range results {
		if errs[i] != nil {
			t.Fatalf("并发解析出错: %v", errs[i])
		}
		if res.Source == JWTSecretSourceEphemeral {
			t.Fatalf("并发首次启动不应降级为临时密钥: %v", res.PersistError)
		}
		if res.Source == JWTSecretSourceGenerated {
			generated++
		}
		if res.Secret != results[0].Secret {
			t.Fatalf("多个进程/连接并发首次启动必须收敛到同一个密钥")
		}
	}
	if generated == 0 {
		t.Fatalf("至少应有一方生成并持久化了密钥")
	}
	if persisted, found := readPersistedJWTSecret(t, stores[0]); !found || persisted != results[0].Secret {
		t.Fatalf("数据库中的密钥应与所有调用方一致")
	}
}

func TestJWTSecret_NotExposedThroughSettings(t *testing.T) {
	st := openFileStore(t, filepath.Join(t.TempDir(), "relaymesh.db"))
	res, err := st.ResolveJWTSecret(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveJWTSecret 失败: %v", err)
	}

	settings, err := st.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings 失败: %v", err)
	}
	appSettings, err := st.GetGlobalAppSettings(context.Background())
	if err != nil {
		t.Fatalf("GetGlobalAppSettings 失败: %v", err)
	}
	for name, value := range map[string]any{"GetSettings": settings, "GetGlobalAppSettings": appSettings} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s 序列化失败: %v", name, err)
		}
		if strings.Contains(string(encoded), res.Secret) {
			t.Fatalf("%s 的返回值泄露了 JWT 签名密钥", name)
		}
	}
}

func TestNewRandomJWTSecret(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		secret, err := newRandomJWTSecret()
		if err != nil {
			t.Fatalf("生成失败: %v", err)
		}
		if len(secret) != 64 {
			t.Fatalf("base64url(48 字节) 应为 64 字符，实际 %d", len(secret))
		}
		if !config.IsStrongJWTSecret(secret) {
			t.Fatalf("生成的密钥必须合格")
		}
		if seen[secret] {
			t.Fatalf("随机密钥不应重复")
		}
		seen[secret] = true
	}
}
