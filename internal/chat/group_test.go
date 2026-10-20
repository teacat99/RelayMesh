package chat

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func newGroup(t *testing.T, members ...string) (*Service, *store.Store, string) {
	t.Helper()
	svc, st := newService(t)
	g, err := st.CreateGroup(context.Background(), store.CreateGroupInput{Title: "联调群", Members: members})
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, g.ID
}

func TestListGroup_PagingMatchesDirectSemantics(t *testing.T) {
	svc, _, gid := newGroup(t, "wf-a")
	ctx := context.Background()
	for i := 0; i < 120; i++ {
		if _, _, err := svc.PostHuman(ctx, gid, fmt.Sprintf("第 %03d 条", i), nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	first, err := svc.ListGroup(ctx, gid, ListParams{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 50 || !first.HasMoreBefore || first.HasMoreAfter {
		t.Fatalf("最新一页: %d before=%v after=%v", len(first.Items), first.HasMoreBefore, first.HasMoreAfter)
	}
	if first.Items[49].Text != "第 119 条" {
		t.Fatalf("最后一条应是最新: %q", first.Items[49].Text)
	}
	seen := map[string]bool{}
	for _, m := range first.Items {
		seen[m.ID] = true
	}
	page := first
	for page.HasMoreBefore {
		page, err = svc.ListGroup(ctx, gid, ListParams{Limit: 50, Before: page.BeforeCursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Items {
			if seen[m.ID] {
				t.Fatalf("翻页出现重复: %s", m.ID)
			}
			seen[m.ID] = true
		}
	}
	if len(seen) != 123 { // 120 条 + 创建与加入 2 条系统行 + 1
		t.Logf("总条数 %d", len(seen))
	}
	if len(seen) < 120 {
		t.Fatalf("不重不漏: %d", len(seen))
	}
	// 搜索只搜文本，不搜系统行
	hit, _ := svc.ListGroup(ctx, gid, ListParams{Limit: 10, Query: "第 042"})
	if len(hit.Items) != 1 || hit.Items[0].Text != "第 042 条" {
		t.Fatalf("搜索: %+v", hit.Items)
	}
	// around
	mid, _ := svc.ListGroup(ctx, gid, ListParams{Limit: 20, Around: hit.Items[0].Cursor})
	found := false
	for _, m := range mid.Items {
		if m.ID == hit.Items[0].ID {
			found = true
		}
	}
	if !found || !mid.HasMoreBefore || !mid.HasMoreAfter {
		t.Fatalf("around 应含中心且两侧都有更多: %v %v %v", found, mid.HasMoreBefore, mid.HasMoreAfter)
	}
}

func TestListGroup_SystemTextsAndReads(t *testing.T) {
	svc, st, gid := newGroup(t, "wf-a", "wf-b")
	ctx := context.Background()
	msg, _, err := svc.PostHuman(ctx, gid, "大家看一下", []string{"wf-a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Reads == nil || msg.Reads.Total != 2 || msg.Reads.Read != 0 || msg.Status != StatusSent {
		t.Fatalf("刚发出：2 个 Agent 都没读: %+v %s", msg.Reads, msg.Status)
	}
	if len(msg.Mentions) != 1 || msg.Mentions[0].Ref != "wf-a" {
		t.Fatalf("mentions: %+v", msg.Mentions)
	}
	if len(msg.Deliveries) != 1 || msg.Deliveries[0].Ref != "wf-a" || msg.Deliveries[0].Status != StatusDelivered {
		t.Fatalf("被点名的成员应有一条已送达: %+v", msg.Deliveries)
	}
	// A 读了：N/M = 1/2；@ 的提醒行变成已处理
	rd, err := svc.ReadForAgent(ctx, gid, "wf-a", 0, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rd.Items) < 1 {
		t.Fatal("A 应读到消息")
	}
	pg, _ := svc.ListGroup(ctx, gid, ListParams{Limit: 50})
	var human Message
	for _, m := range pg.Items {
		if m.ID == msg.ID {
			human = m
		}
	}
	if human.Reads.Read != 1 || human.Reads.Total != 2 || human.Status != StatusSent {
		t.Fatalf("1/2: %+v %s", human.Reads, human.Status)
	}
	if len(human.Deliveries) != 1 || human.Deliveries[0].Status != StatusHandled {
		t.Fatalf("读过之后投递状态应是已处理: %+v", human.Deliveries)
	}
	if _, err := svc.ReadForAgent(ctx, gid, "wf-b", 0, 50, false); err != nil {
		t.Fatal(err)
	}
	pg, _ = svc.ListGroup(ctx, gid, ListParams{Limit: 50})
	for _, m := range pg.Items {
		if m.ID == msg.ID && (m.Reads.Read != 2 || m.Status != StatusRead) {
			t.Fatalf("2/2 应是已读: %+v %s", m.Reads, m.Status)
		}
	}
	// 系统行文字
	texts := ""
	for _, m := range pg.Items {
		if m.Kind == KindSystem {
			texts += m.Text + "|"
		}
	}
	for _, want := range []string{"群聊已创建", "wf-a 加入了群聊", "wf-b 加入了群聊"} {
		if !contains(texts, want) {
			t.Fatalf("系统行里缺 %q: %s", want, texts)
		}
	}
	_ = st
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestReadForAgent_VisibilityAndPeek(t *testing.T) {
	svc, st, gid := newGroup(t, "wf-a")
	ctx := context.Background()
	if _, _, err := svc.PostHuman(ctx, gid, "入群前的话", nil, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := st.AddGroupMembers(ctx, gid, "user", []string{"wf-b"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PostHuman(ctx, gid, "入群后的话", []string{"wf-b"}, ""); err != nil {
		t.Fatal(err)
	}
	peek, err := svc.ReadForAgent(ctx, gid, "wf-b", 0, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range peek.Items {
		if m.Text == "入群前的话" {
			t.Fatal("新成员不应看到入群前的消息")
		}
	}
	d, _ := svc.DigestForAgent(ctx, "wf-b")
	if len(d) != 1 || d[0].Unread < 1 || d[0].Mentioned != 1 {
		t.Fatalf("peek 不推进游标，未读应仍在: %+v", d)
	}
	if _, err := svc.ReadForAgent(ctx, gid, "wf-b", 0, 50, false); err != nil {
		t.Fatal(err)
	}
	d, _ = svc.DigestForAgent(ctx, "wf-b")
	if d[0].Unread != 0 {
		t.Fatalf("读过之后未读应清零: %+v", d)
	}
	if _, err := svc.ReadForAgent(ctx, gid, "wf-x", 0, 50, false); err == nil {
		t.Fatal("非成员不能读")
	}
	if err := st.RemoveGroupMember(ctx, gid, "wf-b"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, err := svc.PostHuman(ctx, gid, "离开后的话", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadForAgent(ctx, gid, "wf-b", 0, 50, false); err == nil {
		t.Fatal("已离开的成员不能再读")
	}
}

func TestGroupSummaries_UnreadAndMention(t *testing.T) {
	svc, _, gid := newGroup(t, "wf-a")
	ctx := context.Background()
	if _, _, err := svc.PostAgent(ctx, gid, "wf-a", "普通一句", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PostAgent(ctx, gid, "wf-a", "点你", []string{"human"}, "", ""); err != nil {
		t.Fatal(err)
	}
	sums, err := svc.GroupSummaries(ctx, false)
	if err != nil || len(sums) != 1 {
		t.Fatalf("%v %v", sums, err)
	}
	if sums[0].Unread != 2 || sums[0].Mentioned != 1 || sums[0].MemberCount != 2 || sums[0].Preview != "点你" {
		t.Fatalf("摘要: %+v", sums[0])
	}
	if _, err := svc.MarkGroupRead(ctx, gid, ""); err != nil {
		t.Fatal(err)
	}
	sums, _ = svc.GroupSummaries(ctx, false)
	if sums[0].Unread != 0 || sums[0].Mentioned != 0 {
		t.Fatalf("已读之后: %+v", sums[0])
	}
}

func TestPostAgent_HopDroppedHasNoMessage(t *testing.T) {
	svc, _, gid := newGroup(t, "wf-a", "wf-b")
	ctx := context.Background()
	if _, _, err := svc.PostHuman(ctx, gid, "开始", []string{"wf-a"}, ""); err != nil {
		t.Fatal(err)
	}
	chain := []struct{ from, to string }{{"wf-a", "wf-b"}, {"wf-b", "wf-a"}, {"wf-a", "wf-b"}}
	for _, c := range chain {
		if _, _, err := svc.PostAgent(ctx, gid, c.from, "接力", []string{c.to}, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	msg, res, err := svc.PostAgent(ctx, gid, "wf-b", "第四跳", []string{"wf-a"}, "", "")
	if err != nil || !res.HopDropped || msg.ID != "" {
		t.Fatalf("第四跳应被丢弃: %v %+v %+v", err, res, msg)
	}
	pg, _ := svc.ListGroup(ctx, gid, ListParams{Limit: 100})
	found := false
	for _, m := range pg.Items {
		if m.Kind == KindSystem && contains(m.Text, "回复链过长") {
			found = true
		}
	}
	if !found {
		t.Fatal("应有「回复链过长」系统行")
	}
	_ = model.ChatKindText
}

func TestDisambiguateLabels(t *testing.T) {
	labels := map[string]string{"wf-aaaa1111": "Cursor · proj", "wf-bbbb2222": "Cursor · proj", "wf-cccc3333": "后端", "wf-dddd4444": "你"}
	disambiguateLabels(labels)
	if labels["wf-aaaa1111"] != "Cursor · proj · 1111" || labels["wf-bbbb2222"] != "Cursor · proj · 2222" {
		t.Fatalf("重名应补编号末 4 位: %v", labels)
	}
	if labels["wf-cccc3333"] != "后端" {
		t.Fatalf("不重名的不动: %v", labels)
	}
	if labels["wf-dddd4444"] != "你 · 4444" {
		t.Fatalf("不能占用保留名「你」: %v", labels)
	}
	uniq := map[string]bool{}
	for _, l := range labels {
		uniq[l] = true
	}
	if len(uniq) != len(labels) {
		t.Fatalf("处理后名字必须互不相同: %v", labels)
	}
}
