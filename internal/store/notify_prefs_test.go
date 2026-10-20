package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/notify"
)

func TestNotifyPrefs_DefaultsAndRoundTrip(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	got, err := st.GetNotifyPrefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.DND || got.NtfyPreview != notify.PreviewAuto || len(got.Conversations) != 0 {
		t.Fatalf("从未设置过时应是出厂默认：%+v", got)
	}

	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	saved, err := st.UpdateNotifyPrefs(ctx, func(cur notify.Prefs) (notify.Prefs, error) {
		cur.DND = true
		cur.NtfyPreview = notify.PreviewNever
		cur.Conversations = map[string]notify.ConvPref{
			"wf:demo": {Policy: notify.PolicyMuted, MutedUntil: &until},
			"grp:g1":  {Policy: notify.PolicyAll},
		}
		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !saved.DND || saved.NtfyPreview != notify.PreviewNever || len(saved.Conversations) != 2 {
		t.Fatalf("保存结果不对：%+v", saved)
	}

	got, err = st.GetNotifyPrefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.DND || got.NtfyPreview != notify.PreviewNever ||
		got.Conversations["wf:demo"].Policy != notify.PolicyMuted ||
		got.Conversations["wf:demo"].MutedUntil == nil || !got.Conversations["wf:demo"].MutedUntil.Equal(until) ||
		got.Conversations["grp:g1"].Policy != notify.PolicyAll {
		t.Fatalf("读回的偏好不对：%+v", got)
	}
}

func TestNotifyPrefs_MutateErrorIsNotWritten(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.UpdateNotifyPrefs(ctx, func(cur notify.Prefs) (notify.Prefs, error) {
		cur.DND = true
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	_, err := st.UpdateNotifyPrefs(ctx, func(cur notify.Prefs) (notify.Prefs, error) {
		cur.DND = false
		return cur, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("mutate 的错误应原样返回：%v", err)
	}
	got, _ := st.GetNotifyPrefs(ctx)
	if !got.DND {
		t.Fatal("mutate 报错时不应落库")
	}
}

func TestNotifyPrefs_ValidationRejectsAndKeepsOld(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.UpdateNotifyPrefs(ctx, func(cur notify.Prefs) (notify.Prefs, error) {
		cur.Conversations = map[string]notify.ConvPref{"wf:keep": {Policy: notify.PolicyMuted}}
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}

	// 超过上限：整体拒绝，原有设置不变。
	_, err := st.UpdateNotifyPrefs(ctx, func(cur notify.Prefs) (notify.Prefs, error) {
		next := notify.Prefs{NtfyPreview: notify.PreviewAuto, Conversations: map[string]notify.ConvPref{}}
		for i := 0; i <= notify.MaxConversations; i++ {
			next.Conversations[fmt.Sprintf("wf:c%d", i)] = notify.ConvPref{Policy: notify.PolicyMuted}
		}
		return next, nil
	})
	if !errors.Is(err, notify.ErrInvalidPrefs) {
		t.Fatalf("超过上限应返回 ErrInvalidPrefs：%v", err)
	}
	got, _ := st.GetNotifyPrefs(ctx)
	if len(got.Conversations) != 1 || got.Conversations["wf:keep"].Policy != notify.PolicyMuted {
		t.Fatalf("被拒绝的修改不应改变库里的值：%+v", got)
	}
}

func TestNotifyPrefs_CorruptValueFallsBackToDefaults(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if err := st.db.Save(&model.SystemSetting{Key: NotifyPrefsKey, Value: "{not json", UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNotifyPrefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.DND || got.NtfyPreview != notify.PreviewAuto || len(got.Conversations) != 0 {
		t.Fatalf("损坏的值应按出厂默认处理（宁可多通知）：%+v", got)
	}
	// 损坏之后仍能正常写入。
	if _, err := st.UpdateNotifyPrefs(ctx, func(cur notify.Prefs) (notify.Prefs, error) {
		cur.DND = true
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetNotifyPrefs(ctx); !got.DND {
		t.Fatal("写入后应能读回")
	}
}

func TestNotifyPrefs_ExpiredMuteDisappearsOnRead(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	raw := fmt.Sprintf(`{"dnd":false,"ntfy_preview":"auto","conversations":{"wf:old":{"policy":"muted","muted_until":%q},"wf:forever":{"policy":"muted"}}}`, past.Format(time.RFC3339))
	if err := st.db.Save(&model.SystemSetting{Key: NotifyPrefsKey, Value: raw, UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNotifyPrefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Conversations["wf:old"]; ok {
		t.Error("过期的静音不应再出现")
	}
	if got.Conversations["wf:forever"].Policy != notify.PolicyMuted {
		t.Error("没有期限的静音应保留")
	}
}

func TestNotifyPrefs_ConcurrentUpdatesDoNotLoseWrites(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.UpdateNotifyPrefs(ctx, func(cur notify.Prefs) (notify.Prefs, error) {
				if cur.Conversations == nil {
					cur.Conversations = map[string]notify.ConvPref{}
				}
				cur.Conversations[fmt.Sprintf("wf:c%d", i)] = notify.ConvPref{Policy: notify.PolicyMuted}
				return cur, nil
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.GetNotifyPrefs(ctx)
	if len(got.Conversations) != n {
		t.Fatalf("并发修改丢了写入：期望 %d 条，得到 %d", n, len(got.Conversations))
	}
}
