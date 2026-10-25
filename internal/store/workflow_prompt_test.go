package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestNormalizePromptText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"统一换行并去首尾空白", "  第一行\r\n第二行\r第三行  \n", "第一行\n第二行\n第三行"},
		{"保留制表符与换行", "a\tb\nc", "a\tb\nc"},
		{"去掉 NUL 与其他控制字符", "a\x00b\x07c\x1bd", "abcd"},
		{"空白串等于清除", " \n\t ", ""},
		{"中文标点不受影响", "【会话常驻规则】\n1. 先问再做。", "【会话常驻规则】\n1. 先问再做。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePromptText(tc.in, 100)
			if err != nil || got != tc.want {
				t.Fatalf("NormalizePromptText(%q) = %q, %v；期望 %q", tc.in, got, err, tc.want)
			}
		})
	}
	// 字数按字符计，不是字节：1200 个汉字（3600 字节）合法，1201 个被拒。
	if _, err := NormalizePromptText(strings.Repeat("字", model.ResidentPromptMax), model.ResidentPromptMax); err != nil {
		t.Fatalf("1200 个汉字应当合法：%v", err)
	}
	_, err := NormalizePromptText(strings.Repeat("字", model.ResidentPromptMax+1), model.ResidentPromptMax)
	var ae *AppError
	if !errors.As(err, &ae) || ae.Kind != ErrInvalidInput || !strings.Contains(ae.Message, "1201") {
		t.Fatalf("超长应返回带字数的 invalid input：%v", err)
	}
}

func TestResidentPrompt_SaveBumpsRevisionOnlyOnChange(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	// 没配置过：零值，版本 0。
	row, err := st.GetWorkflowPrompt(ctx, "wf-a")
	if err != nil || row.ResidentRev != 0 || row.Resident != "" || row.WorkflowID != "wf-a" {
		t.Fatalf("没配置过应得到零值：%+v %v", row, err)
	}
	// 从没配置过的会话保存空串：什么也不做。
	if _, changed, err := st.SaveResidentPrompt(ctx, "wf-a", "  ", "me", now); err != nil || changed {
		t.Fatalf("空串保存不该产生版本：changed=%v err=%v", changed, err)
	}
	if revs, _ := st.ListPromptRevisions(ctx, "wf-a", model.PromptFieldResident); len(revs) != 0 {
		t.Fatalf("不该有历史版本：%+v", revs)
	}

	row, changed, err := st.SaveResidentPrompt(ctx, "wf-a", "规则一", "alice", now)
	if err != nil || !changed || row.ResidentRev != 1 || row.Resident != "规则一" || row.UpdatedBy != "alice" {
		t.Fatalf("首次保存：%+v changed=%v err=%v", row, changed, err)
	}
	// 同样的内容再保存：不涨版本。
	row, changed, err = st.SaveResidentPrompt(ctx, "wf-a", "规则一\n", "alice", now.Add(time.Minute))
	if err != nil || changed || row.ResidentRev != 1 {
		t.Fatalf("内容没变不该涨版本：%+v changed=%v err=%v", row, changed, err)
	}
	row, changed, _ = st.SaveResidentPrompt(ctx, "wf-a", "规则二", "bob", now.Add(2*time.Minute))
	if !changed || row.ResidentRev != 2 || row.UpdatedBy != "bob" {
		t.Fatalf("改内容应涨到 2：%+v", row)
	}
	// 清除也算一个版本：让智能体知道「常驻指令已清除」。
	row, changed, _ = st.SaveResidentPrompt(ctx, "wf-a", "", "bob", now.Add(3*time.Minute))
	if !changed || row.ResidentRev != 3 || row.Resident != "" {
		t.Fatalf("清除应涨到 3：%+v", row)
	}
	revs, err := st.ListPromptRevisions(ctx, "wf-a", model.PromptFieldResident)
	if err != nil || len(revs) != 3 || revs[0].Rev != 3 || revs[2].Rev != 1 || revs[1].Text != "规则二" {
		t.Fatalf("历史版本应新的在前：%+v %v", revs, err)
	}
	// 别的工作流互不影响。
	if other, _ := st.GetWorkflowPrompt(ctx, "wf-b"); other.ResidentRev != 0 {
		t.Fatalf("别的工作流不该有版本：%+v", other)
	}
}

func TestResidentPrompt_TooLongIsRejectedWithoutSideEffects(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, _, err := st.SaveResidentPrompt(ctx, "wf-a", "旧版本", "me", now); err != nil {
		t.Fatal(err)
	}
	_, _, err := st.SaveResidentPrompt(ctx, "wf-a", strings.Repeat("长", model.ResidentPromptMax+1), "me", now)
	var ae *AppError
	if !errors.As(err, &ae) || ae.Kind != ErrInvalidInput {
		t.Fatalf("超长应被拒：%v", err)
	}
	row, _ := st.GetWorkflowPrompt(ctx, "wf-a")
	if row.Resident != "旧版本" || row.ResidentRev != 1 {
		t.Fatalf("被拒的保存不该改动现有内容：%+v", row)
	}
	if _, _, err := st.SaveResidentPrompt(ctx, "  ", "x", "me", now); err == nil {
		t.Fatal("workflow_id 为空应报错")
	}
}

func TestResidentPrompt_RevisionsKeepOnlyTheLatestTen(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()
	for i := 1; i <= model.PromptRevisionKeep+4; i++ {
		if _, changed, err := st.SaveResidentPrompt(ctx, "wf-a", strings.Repeat("v", i), "me", now.Add(time.Duration(i)*time.Second)); err != nil || !changed {
			t.Fatalf("第 %d 次保存：changed=%v err=%v", i, changed, err)
		}
	}
	// 另一个工作流的历史不受裁剪影响。
	if _, _, err := st.SaveResidentPrompt(ctx, "wf-b", "别的会话", "me", now); err != nil {
		t.Fatal(err)
	}
	revs, err := st.ListPromptRevisions(ctx, "wf-a", model.PromptFieldResident)
	if err != nil || len(revs) != model.PromptRevisionKeep {
		t.Fatalf("应只留最近 %d 个：%d %v", model.PromptRevisionKeep, len(revs), err)
	}
	if revs[0].Rev != model.PromptRevisionKeep+4 || revs[len(revs)-1].Rev != 5 {
		t.Fatalf("留下的应是第 5～14 版：%d..%d", revs[len(revs)-1].Rev, revs[0].Rev)
	}
	if other, _ := st.ListPromptRevisions(ctx, "wf-b", model.PromptFieldResident); len(other) != 1 {
		t.Fatalf("别的工作流的历史不该被裁：%+v", other)
	}
}

func TestResidentPrompt_MarkToldOnlyMovesForward(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()
	// 没有行时标记不报错、也不凭空造行。
	if err := st.MarkResidentTold(ctx, "wf-a", 3); err != nil {
		t.Fatal(err)
	}
	if row, _ := st.GetWorkflowPrompt(ctx, "wf-a"); row.ResidentToldRev != 0 || row.ResidentRev != 0 {
		t.Fatalf("没有行时不该凭空造出：%+v", row)
	}
	_, _, _ = st.SaveResidentPrompt(ctx, "wf-a", "a", "me", now)
	_, _, _ = st.SaveResidentPrompt(ctx, "wf-a", "b", "me", now)
	if err := st.MarkResidentTold(ctx, "wf-a", 2); err != nil {
		t.Fatal(err)
	}
	if row, _ := st.GetWorkflowPrompt(ctx, "wf-a"); row.ResidentToldRev != 2 {
		t.Fatalf("应追平到 2：%+v", row)
	}
	_ = st.MarkResidentTold(ctx, "wf-a", 1) // 往回标记被忽略
	if row, _ := st.GetWorkflowPrompt(ctx, "wf-a"); row.ResidentToldRev != 2 {
		t.Fatalf("不该往回改：%+v", row)
	}
	// 保存新版本不会动「已告知」的版本：它落后于当前版本，正是「还没告诉智能体」的信号。
	_, _, _ = st.SaveResidentPrompt(ctx, "wf-a", "c", "me", now)
	if row, _ := st.GetWorkflowPrompt(ctx, "wf-a"); row.ResidentRev != 3 || row.ResidentToldRev != 2 {
		t.Fatalf("保存新版本后已告知版本应落后：%+v", row)
	}
}

func TestResidentPrompt_RestoreRevisionSavesAsNewVersion(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()
	_, _, _ = st.SaveResidentPrompt(ctx, "wf-a", "最早的规则", "me", now)
	_, _, _ = st.SaveResidentPrompt(ctx, "wf-a", "后来的规则", "me", now.Add(time.Second))
	revs, _ := st.ListPromptRevisions(ctx, "wf-a", model.PromptFieldResident)
	first := revs[len(revs)-1]
	row, changed, err := st.RestoreResidentRevision(ctx, "wf-a", first.ID, "carol", now.Add(2*time.Second))
	if err != nil || !changed || row.Resident != "最早的规则" || row.ResidentRev != 3 || row.UpdatedBy != "carol" {
		t.Fatalf("回滚应存成第 3 版：%+v changed=%v err=%v", row, changed, err)
	}
	// 回滚到的内容与当前一致：什么也不做。
	if _, changed, err := st.RestoreResidentRevision(ctx, "wf-a", first.ID, "carol", now.Add(3*time.Second)); err != nil || changed {
		t.Fatalf("回滚到当前内容不该产生版本：changed=%v err=%v", changed, err)
	}
	// 别的工作流的版本不能拿来回滚；不存在的版本是 404。
	_, _, _ = st.SaveResidentPrompt(ctx, "wf-b", "别人的", "me", now)
	otherRevs, _ := st.ListPromptRevisions(ctx, "wf-b", model.PromptFieldResident)
	var ae *AppError
	if _, _, err := st.RestoreResidentRevision(ctx, "wf-a", otherRevs[0].ID, "me", now); !errors.As(err, &ae) || ae.Kind != ErrNotFound {
		t.Fatalf("别的工作流的版本不能回滚：%v", err)
	}
	if _, _, err := st.RestoreResidentRevision(ctx, "wf-a", 99999, "me", now); !errors.As(err, &ae) || ae.Kind != ErrNotFound {
		t.Fatalf("不存在的版本应是 404：%v", err)
	}
}
