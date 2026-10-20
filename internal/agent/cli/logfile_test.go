package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotatingFileRotatesOnSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "agent.log")
	w, err := openRotatingFileWith(path, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	line := strings.Repeat("x", 39) + "\n" // 40 字节
	for i := 0; i < 5; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	cur, _ := os.ReadFile(path)
	old, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("应生成轮转文件: %v", err)
	}
	if len(cur) > 100 || len(old) > 100 {
		t.Fatalf("单个文件不应超过上限: cur=%d old=%d", len(cur), len(old))
	}
	if len(cur)+len(old) < 120 {
		t.Fatalf("最近的日志不应丢失: cur=%d old=%d", len(cur), len(old))
	}
}

func TestRotatingFileAppendsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	for _, s := range []string{"first\n", "second\n"} {
		w, err := openRotatingFileWith(path, 1<<20, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(s))
		_ = w.Close()
	}
	got, _ := os.ReadFile(path)
	if string(got) != "first\nsecond\n" {
		t.Fatalf("重启后应追加而不是覆盖: %q", got)
	}
}

func TestRotatingFileConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, err := openRotatingFileWith(path, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = w.Write([]byte("line\n"))
			}
		}()
	}
	wg.Wait()
	got, _ := os.ReadFile(path)
	if n := strings.Count(string(got), "line\n"); n != 400 {
		t.Fatalf("并发写入丢了行: %d/400", n)
	}
}

func TestRunWritesBannerAndLogsToLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "agent.log")
	env := testEnv(t)
	stdout, stderr := env.Stdout.(*syncBuffer), env.Stderr.(*syncBuffer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即结束：只验证启动阶段的输出去向
	code := cmdRun(ctx, []string{"--server", "https://relay.example.com", "--temp", "--no-tofu", "--log-file", path}, env)
	if code != 0 {
		t.Fatalf("退出码 %d\nstderr: %s", code, stderr.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "机器码") || !strings.Contains(string(got), "Agent 已退出") {
		t.Fatalf("横幅与日志应写入文件:\n%s", got)
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Fatalf("指定 --log-file 后不应再写控制台: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRotatingFilePreambleOnlyOnNewFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	bom := []byte{0xEF, 0xBB, 0xBF}
	for _, s := range []string{"中文一\n", "中文二\n"} {
		w, err := openRotatingFileWith(path, 1<<20, bom)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(s))
		_ = w.Close()
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(bom)+"中文一\n中文二\n" {
		t.Fatalf("BOM 只应出现在新建文件开头，重启追加时不能重复: %q", got)
	}
}

func TestRotatingFilePreambleSurvivesRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	bom := []byte{0xEF, 0xBB, 0xBF}
	w, err := openRotatingFileWith(path, 100, bom)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	line := strings.Repeat("x", 39) + "\n"
	for i := 0; i < 5; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{path, path + ".1"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(b), string(bom)) || len(b) > 100 {
			t.Fatalf("%s：每一代都应以 BOM 开头且不超过上限: len=%d", p, len(b))
		}
	}
}

func TestPreambleAloneDoesNotTriggerRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, err := openRotatingFileWith(path, 10, []byte{0xEF, 0xBB, 0xBF})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// 单条日志就超过上限：新文件里只有 BOM 时，不应为了它轮转出一个只有 BOM 的空文件。
	if _, err := w.Write([]byte(strings.Repeat("y", 50))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("不应生成只含 BOM 的轮转文件")
	}
}
