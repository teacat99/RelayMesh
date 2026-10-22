package store

import (
	"strconv"
	"strings"
	"testing"
)

func TestArtifactDataLimitsAndVersions(t *testing.T) {
	st, ctx := projTestStore(t)
	set := func(key, val string, ev *int) error {
		_, err := st.ArtifactDataSet(ctx, ArtifactDataSetInput{DocID: 1, Key: key, ValueJSON: val, ExpectedVersion: ev, ActorLabel: "测试"})
		return err
	}
	zero, one := 0, 1
	if err := set("board", `{"todo":[]}`, &zero); err != nil {
		t.Fatalf("期望不存在时创建: %v", err)
	}
	wantAppErr(t, set("board", `{}`, &zero), "已被别人更新")
	if err := set("board", `{"todo":["a"]}`, &one); err != nil {
		t.Fatal(err)
	}
	row, err := st.ArtifactDataGet(ctx, 1, "board")
	if err != nil || row.Version != 2 || !strings.Contains(row.Value, `"a"`) {
		t.Fatalf("版本应递增并存下新值: %+v %v", row, err)
	}
	wantAppErr(t, set("bad key", `1`, nil), "键名")
	wantAppErr(t, set("k", `{oops`, nil), "合法的 JSON")
	if _, err := st.ArtifactDataGet(ctx, 1, "nope"); err == nil {
		t.Fatal("读不存在的键应报错")
	}
	// 不同文档互不影响。
	if err := set("board", `1`, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArtifactDataSet(ctx, ArtifactDataSetInput{DocID: 2, Key: "board", ValueJSON: `2`}); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.ArtifactDataGet(ctx, 2, "board"); r.Value != "2" || r.Version != 1 {
		t.Fatalf("文档 2 的数据独立: %+v", r)
	}

	// 键数上限 200：已有 1 个键（board），再加 199 个正好，第 201 个被拒。
	for i := 0; i < 199; i++ {
		if err := set("k"+strconv.Itoa(i), `0`, nil); err != nil {
			t.Fatalf("第 %d 个键应成功: %v", i, err)
		}
	}
	wantAppErr(t, set("overflow", `0`, nil), "键数")
	// 已存在的键仍可改。
	if err := set("k0", `1`, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ArtifactDataDelete(ctx, 1, "k0"); !ok || err != nil {
		t.Fatalf("删除: %v %v", ok, err)
	}
	if err := set("overflow", `0`, nil); err != nil {
		t.Fatalf("删掉一个后可再加: %v", err)
	}

	// 总量上限：大值写进来后，总量超限被拒。
	big := `"` + strings.Repeat("x", 200*1024) + `"`
	if _, err := st.ArtifactDataSet(ctx, ArtifactDataSetInput{DocID: 3, Key: "a", ValueJSON: big}); err != nil {
		t.Fatalf("200KB 应可写: %v", err)
	}
	_, err = st.ArtifactDataSet(ctx, ArtifactDataSetInput{DocID: 3, Key: "b", ValueJSON: big})
	wantAppErr(t, err, "256 KB")
	// 把 a 改小之后 b 就能写。
	if _, err := st.ArtifactDataSet(ctx, ArtifactDataSetInput{DocID: 3, Key: "a", ValueJSON: `"s"`}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArtifactDataSet(ctx, ArtifactDataSetInput{DocID: 3, Key: "b", ValueJSON: big}); err != nil {
		t.Fatalf("a 改小后 b 应可写: %v", err)
	}
}

func TestArtifactComments(t *testing.T) {
	st, ctx := projTestStore(t)
	c1, err := st.AddArtifactComment(ctx, ArtifactCommentInput{DocID: 7, Anchor: " card-1 ", Body: " 这里要改 ", AuthorLabel: "你"})
	if err != nil || c1.Anchor != "card-1" || c1.Body != "这里要改" || c1.AuthorKind != "human" {
		t.Fatalf("新增: %+v %v", c1, err)
	}
	_, err = st.AddArtifactComment(ctx, ArtifactCommentInput{DocID: 7, Body: "  "})
	wantAppErr(t, err, "评论内容")
	_, err = st.AddArtifactComment(ctx, ArtifactCommentInput{DocID: 7, Body: strings.Repeat("长", 4001)})
	wantAppErr(t, err, "过长")
	c2, _ := st.AddArtifactComment(ctx, ArtifactCommentInput{DocID: 7, Body: "第二条"})
	if n := st.CountOpenArtifactComments(ctx, 7); n != 2 {
		t.Fatalf("未解决 2 条: %d", n)
	}
	if _, err := st.ResolveArtifactComment(ctx, 8, c1.ID, true); err == nil {
		t.Fatal("别的文档的评论不能被解决")
	}
	if r, err := st.ResolveArtifactComment(ctx, 7, c1.ID, true); err != nil || !r.Resolved || r.ResolvedAt == nil {
		t.Fatalf("解决: %+v %v", r, err)
	}
	open, _ := st.ListArtifactComments(ctx, 7, false, 0)
	if len(open) != 1 || open[0].ID != c2.ID {
		t.Fatalf("默认只列未解决: %+v", open)
	}
	all, _ := st.ListArtifactComments(ctx, 7, true, 0)
	if len(all) != 2 || all[0].ID != c1.ID {
		t.Fatalf("含已解决按时间正序: %+v", all)
	}
	if r, _ := st.ResolveArtifactComment(ctx, 7, c1.ID, false); r.Resolved || r.ResolvedAt != nil {
		t.Fatalf("重新打开: %+v", r)
	}
}
