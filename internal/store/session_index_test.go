package store

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 「会话表热点查询必须走索引」的守卫（#177 页面数据加载核查）。
//
// 背景：feedback_sessions 的行很重（汇报正文常有几 KB，超过一页的部分存在溢出页里，`status` / `created_at` 这些靠后的列要把溢出链读完才拿得到），
// 所以「整表扫描」的代价按**数据总量**算而不是按行数算：生产库 30 多 MB 时，每次 GET /sessions/current（各页面都在轮询）都要读几十 MB。
// 规模实验（scripts/dev/seed-scale，5 万行）实测：current 273 ms → 0.15 ms，窗口查询 531 ms → 3.8 ms。
// 这些索引是向后兼容的「只增」索引（旧版本照常读写），这里把它们钉住：谁以后改了查询或删了索引，这里会先红。

// planOf 对一条已展开参数的 SELECT 取 EXPLAIN QUERY PLAN 的各行 detail。
func planOf(t *testing.T, st *Store, sqlText string) []string {
	t.Helper()
	rows, err := st.db.Raw("EXPLAIN QUERY PLAN " + sqlText).Rows()
	if err != nil {
		t.Fatalf("EXPLAIN 失败: %v\nSQL: %s", err, sqlText)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("读取 EXPLAIN 行失败: %v", err)
		}
		out = append(out, detail)
	}
	return out
}

// 整表扫描 = 「SCAN 表」后面没有 USING（带 USING 的是走索引整条扫，与「整表」是两回事）。
var reFullTableScan = regexp.MustCompile(`^SCAN (TABLE )?feedback_sessions( AS \w+)?$`)

// selectsDuring 返回 fn 执行期间 Hub 发出的、涉及 feedback_sessions 的 SELECT（来自 SQL 追踪文件）。
func selectsDuring(t *testing.T, tracePath string, fn func()) []string {
	t.Helper()
	before := len(readTraceLines(t, tracePath))
	fn()
	all := readTraceLines(t, tracePath)
	var out []string
	for _, l := range all[before:] {
		s := strings.TrimSpace(l.SQL)
		if strings.HasPrefix(strings.ToUpper(s), "SELECT") && strings.Contains(s, "feedback_sessions") {
			out = append(out, s)
		}
	}
	return out
}

func seedSessionsForPlan(t *testing.T, st *Store) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 12; i++ {
		status := "completed"
		if i == 11 {
			status = "pending"
		}
		s := model.FeedbackSession{
			ID:         fmt.Sprintf("sess-plan-%02d", i),
			WorkflowID: fmt.Sprintf("wf-plan-%d", i%3),
			Title:      "t",
			Summary:    strings.Repeat("汇报正文 ", 50),
			Status:     status,
			CreatedAt:  base.Add(time.Duration(i) * time.Minute),
		}
		if err := st.db.Create(&s).Error; err != nil {
			t.Fatalf("写入会话失败: %v", err)
		}
	}
}

func TestFeedbackSessionIndexesExist(t *testing.T) {
	st := setupTestStore(t)
	m := st.db.Migrator()
	for _, name := range []string{"idx_fs_wf_created", "idx_fs_created", "idx_fs_status_created"} {
		if !m.HasIndex(&model.FeedbackSession{}, name) {
			t.Errorf("feedback_sessions 缺少索引 %s", name)
		}
	}
}

// 侧栏窗口查询与「当前待答会话」都必须走索引：不能整表扫描，窗口查询也不能再整体临时排序。
func TestFeedbackSessionHotQueriesUseIndexes(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv(SQLTraceEnv, trace)
	st := setupTestStore(t)
	seedSessionsForPlan(t, st)
	ctx := context.Background()

	check := func(name string, fn func(), wantIndex string, forbidFullSort bool) {
		t.Helper()
		sels := selectsDuring(t, trace, fn)
		if len(sels) == 0 {
			t.Fatalf("%s：追踪里没抓到针对 feedback_sessions 的 SELECT（追踪开关失效？）", name)
		}
		var sawWanted bool
		for _, s := range sels {
			plan := planOf(t, st, s)
			for _, d := range plan {
				if reFullTableScan.MatchString(d) {
					t.Errorf("%s：整表扫描（%q）\nSQL: %s\n计划: %s", name, d, s, strings.Join(plan, " | "))
				}
				if forbidFullSort && strings.Contains(d, "USE TEMP B-TREE FOR ORDER BY") {
					t.Errorf("%s：整体临时排序（%q）\nSQL: %s\n计划: %s", name, d, s, strings.Join(plan, " | "))
				}
				if wantIndex != "" && strings.Contains(d, wantIndex) {
					sawWanted = true
				}
			}
		}
		if wantIndex != "" && !sawWanted {
			t.Errorf("%s：没有用到索引 %s；各查询计划：%v", name, wantIndex, sels)
		}
	}

	check("侧栏窗口 ListFeedbackSessionsBrief", func() {
		if _, err := st.ListFeedbackSessionsBrief(ctx, "", "", 200); err != nil {
			t.Fatal(err)
		}
	}, "idx_fs_created", true)

	// 补「每个工作流最新一行」的那条 JOIN 查询：子查询走 idx_fs_wf_created 覆盖索引（只读索引、不碰行本体），
	// 结果集每个工作流 1 行，临时排序的是工作流个数而不是会话行数，所以这里只禁整表扫描，不禁这一小步排序。
	check("侧栏 ListFeedbackSessionsBriefCovering", func() {
		if _, err := st.ListFeedbackSessionsBriefCovering(ctx, "", "", 200); err != nil {
			t.Fatal(err)
		}
	}, "idx_fs_created", false)

	check("当前待答会话 GetCurrentFeedbackSession", func() {
		if _, err := st.GetCurrentFeedbackSession(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}, "idx_fs_status_created", false)
}

// 对照组：把新增的两个索引删掉，同样的查询必须被上面的判据认出是「整表扫描」——证明守卫不是空转，
// 也留一份「没有索引时计划长什么样」的记录（出处：规模实验里 current 从 0.15 ms 退化到 273 ms 的就是这条计划）。
func TestFeedbackSessionIndexGuard_DetectsMissingIndexes(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv(SQLTraceEnv, trace)
	st := setupTestStore(t)
	seedSessionsForPlan(t, st)
	for _, name := range []string{"idx_fs_created", "idx_fs_status_created"} {
		if err := st.db.Exec("DROP INDEX IF EXISTS " + name).Error; err != nil {
			t.Fatalf("删除索引 %s 失败: %v", name, err)
		}
	}
	sels := selectsDuring(t, trace, func() {
		if _, err := st.GetCurrentFeedbackSession(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
	})
	if len(sels) == 0 {
		t.Fatal("没抓到 SELECT")
	}
	var fullScan bool
	for _, s := range sels {
		for _, d := range planOf(t, st, s) {
			if reFullTableScan.MatchString(d) {
				fullScan = true
			}
		}
	}
	if !fullScan {
		t.Fatalf("没有索引时应被判为整表扫描，判据可能失效了；SQL: %v", sels)
	}
}
