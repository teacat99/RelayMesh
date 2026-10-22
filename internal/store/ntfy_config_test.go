package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
)

func TestNtfyConfig_DefaultsAndRoundTrip(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	got, err := st.GetNtfyConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Server != ntfy.DefaultServer || got.Topic != "" || got.Priority != ntfy.DefaultPriority {
		t.Fatalf("从未配置过时应是关闭的默认值：%+v", got)
	}

	saved, err := st.UpdateNtfyConfig(ctx, func(cur ntfy.Config) ntfy.Config {
		cur.Enabled = true
		cur.Server = "https://ntfy.example.com/"
		cur.Topic = "rm-abc123"
		cur.Token = "tk_secret"
		cur.DelaySeconds = 60
		return cur
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Server != "https://ntfy.example.com" {
		t.Errorf("保存的配置应已规整：%+v", saved)
	}

	got, err = st.GetNtfyConfig(ctx)
	if err != nil || !got.Enabled || got.Topic != "rm-abc123" || got.Token != "tk_secret" || got.DelaySeconds != 60 {
		t.Fatalf("读回的配置不对：%+v err=%v", got, err)
	}
}

func TestNtfyConfig_InvalidIsRejectedAndNotWritten(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.UpdateNtfyConfig(ctx, func(cur ntfy.Config) ntfy.Config {
		cur.Enabled, cur.Topic = true, "good-topic"
		return cur
	}); err != nil {
		t.Fatal(err)
	}

	// 启用时主题必填；不合法的改动整体不落库。
	_, err := st.UpdateNtfyConfig(ctx, func(cur ntfy.Config) ntfy.Config {
		cur.Topic = ""
		cur.Server = "https://changed.example.com"
		return cur
	})
	if !errors.Is(err, ntfy.ErrInvalidConfig) {
		t.Fatalf("应返回 ErrInvalidConfig：%v", err)
	}
	got, _ := st.GetNtfyConfig(ctx)
	if got.Topic != "good-topic" || got.Server != ntfy.DefaultServer {
		t.Fatalf("校验失败不应写入任何改动：%+v", got)
	}

	// 未启用时，只填了一半的配置可以先存着。
	if _, err := st.UpdateNtfyConfig(ctx, func(cur ntfy.Config) ntfy.Config {
		cur.Enabled, cur.Topic = false, ""
		return cur
	}); err != nil {
		t.Fatalf("未启用时允许保存没有主题的配置：%v", err)
	}
}

func TestNtfyConfig_CorruptValueFallsBackToDisabled(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if err := st.db.Save(&model.SystemSetting{Key: NtfyConfigKey, Value: "{not json", UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNtfyConfig(ctx)
	if err != nil || got.Enabled || got.Topic != "" {
		t.Fatalf("损坏的值应按「未配置」处理：%+v err=%v", got, err)
	}
	// 损坏之后仍能正常保存。
	if _, err := st.UpdateNtfyConfig(ctx, func(cur ntfy.Config) ntfy.Config { cur.Topic = "t1"; return cur }); err != nil {
		t.Fatal(err)
	}
}

// ntfy 的配置含凭据，不能混进整体返回给浏览器的全局设置里。
func TestNtfyConfig_NotInGlobalSettings(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.UpdateNtfyConfig(ctx, func(cur ntfy.Config) ntfy.Config {
		cur.Token, cur.Topic = "tk_must_not_leak", "t1"
		return cur
	}); err != nil {
		t.Fatal(err)
	}
	settings, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range settings {
		if k == "ntfy" || k == NtfyConfigKey {
			t.Errorf("全局设置里不应出现 ntfy 配置：%s=%v", k, v)
		}
	}
}
