package store

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTraceLines(t *testing.T, path string) []sqlTraceLine {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开追踪文件失败: %v", err)
	}
	defer f.Close()
	var out []sqlTraceLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var l sqlTraceLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("追踪文件里有一行不是合法 JSON: %v: %s", err, sc.Text())
		}
		out = append(out, l)
	}
	return out
}

// 默认不追踪：环境变量为空时 gorm 用的仍是静默日志器，不会打开任何追踪文件（生产与所有普通测试都走这条路）。
func TestSQLTrace_OffByDefault(t *testing.T) {
	t.Setenv(SQLTraceEnv, "")
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := st.db.Config.Logger.(*sqlTraceLogger); ok {
		t.Fatal("环境变量为空时不该装上 SQL 追踪日志器")
	}
	if _, err := st.ListFeedbackSessionsBrief(context.Background(), "", "", 10); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
}

// 设置环境变量后装上的确实是追踪日志器（与上一条互为对照）。
func TestSQLTrace_OnWhenEnvSet(t *testing.T) {
	t.Setenv(SQLTraceEnv, filepath.Join(t.TempDir(), "trace.jsonl"))
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := st.db.Config.Logger.(*sqlTraceLogger); !ok {
		t.Fatal("设置环境变量后应装上 SQL 追踪日志器")
	}
}

// 设置环境变量后：每条 SQL 一行 JSON，带时间 / 耗时 / 行数 / SQL 原文（SELECT 的 WHERE 与 ORDER 都在）。
func TestSQLTrace_WritesOneLinePerStatement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv(SQLTraceEnv, path)
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before := len(readTraceLines(t, path)) // 建表 / 迁移本身也会产生语句
	if _, err := st.ListFeedbackSessionsBrief(context.Background(), "/tmp/p", "pending", 7); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	lines := readTraceLines(t, path)
	if len(lines) != before+1 {
		t.Fatalf("一次查询应恰好多一行追踪: 之前 %d 之后 %d", before, len(lines))
	}
	last := lines[len(lines)-1]
	for _, want := range []string{"SELECT", "feedback_sessions", "project_directory = \"/tmp/p\"", "status = \"pending\"", "ORDER BY created_at DESC", "LIMIT 7"} {
		if !strings.Contains(strings.ReplaceAll(last.SQL, "'", "\""), want) {
			t.Errorf("追踪的 SQL 里应含 %q，实际: %s", want, last.SQL)
		}
	}
	if last.At == "" || last.MS < 0 || last.Err != "" {
		t.Errorf("时间 / 耗时 / 报错字段不对: %+v", last)
	}
}

// 「没找到」不是错误；过长的 SQL 截断。
func TestSQLTrace_NotFoundIsNotAnErrorAndLongSQLIsTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv(SQLTraceEnv, path)
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := st.GetCurrentFeedbackSession(context.Background(), ""); err != nil {
		t.Fatalf("没有 pending 会话应返回 nil, nil: %v", err)
	}
	long := strings.Repeat("长", sqlTraceMaxLen*2)
	if err := st.DB().Exec("SELECT ?", long).Error; err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	lines := readTraceLines(t, path)
	var sawCurrent, sawLong bool
	for _, l := range lines {
		if strings.Contains(l.SQL, "status = 'pending'") || strings.Contains(l.SQL, `status = "pending"`) {
			sawCurrent = true
			if l.Err != "" {
				t.Errorf("没找到不该记成错误: %+v", l)
			}
		}
		if strings.HasSuffix(l.SQL, "…[截断]") {
			sawLong = true
			if n := len([]rune(l.SQL)); n > sqlTraceMaxLen+len([]rune("…[截断]")) {
				t.Errorf("截断后仍过长: %d 个字符", n)
			}
		}
	}
	if !sawCurrent {
		t.Error("应追踪到 GetCurrentFeedbackSession 的查询")
	}
	if !sawLong {
		t.Error("过长的 SQL 应被截断并带「…[截断]」标记")
	}
}

// 追踪文件打不开就明确报错，而不是悄悄不追踪（实验里设了环境变量就是要它生效）。
func TestSQLTrace_BadPathFailsLoudly(t *testing.T) {
	t.Setenv(SQLTraceEnv, filepath.Join(t.TempDir(), "no-such-dir", "trace.jsonl"))
	if _, err := New(":memory:"); err == nil {
		t.Fatal("追踪文件路径不可写时 New 应返回错误")
	}
}
