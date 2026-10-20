package sshexec

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
)

// jobEnv 把脚本经真实的 SSH 协议送到本机的 /bin/sh 上执行（HOME 指向测试临时目录）。
type jobEnv struct {
	t    *testing.T
	srv  *sshdtest.Server
	pool *Pool
	spec Spec
}

func newJobEnv(t *testing.T) *jobEnv {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("需要 POSIX sh")
	}
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword})
	p := &Pool{DialTimeout: 3 * time.Second}
	t.Cleanup(p.Close)
	return &jobEnv{t: t, srv: srv, pool: p, spec: passwordSpec(srv, "tester")}
}

func (e *jobEnv) run(cmd string, stdin []byte) Result {
	e.t.Helper()
	res, err := e.pool.Exec(context.Background(), "h", e.spec, cmd, stdin, 0)
	if err != nil {
		e.t.Fatalf("远端执行失败: %v", err)
	}
	return res
}

func newRunID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func (e *jobEnv) launch(sp LaunchSpec) LaunchResult {
	e.t.Helper()
	cmd, stdin, err := LaunchCommand(sp)
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := ParseLaunch(e.run(cmd, stdin).Stdout)
	if err != nil {
		e.t.Fatalf("解析启动输出失败: %v", err)
	}
	return r
}

func (e *jobEnv) poll(id string, outOff, errOff int64) PollResult {
	e.t.Helper()
	cmd, stdin, err := PollCommand(PollSpec{RunID: id, OutOff: outOff, ErrOff: errOff})
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := ParsePoll(e.run(cmd, stdin).Stdout)
	if err != nil {
		e.t.Fatalf("解析轮询输出失败: %v", err)
	}
	return r
}

// waitFinished 轮询到命令结束并取完两路输出，返回最后一次轮询结果与累计输出。
func (e *jobEnv) waitFinished(id string, within time.Duration) (PollResult, string, string) {
	e.t.Helper()
	var out, errs strings.Builder
	var oo, eo int64
	deadline := time.Now().Add(within)
	for {
		r := e.poll(id, oo, eo)
		out.Write(r.Out)
		errs.Write(r.Err)
		oo += int64(len(r.Out))
		eo += int64(len(r.Err))
		if r.ExitCode != nil && oo >= r.OutSize && eo >= r.ErrSize {
			return r, out.String(), errs.String()
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("等待运行结束超时: %+v", r)
		}
		time.Sleep(80 * time.Millisecond)
	}
}

func TestLaunchPollAndFinish(t *testing.T) {
	e := newJobEnv(t)
	id := newRunID(t)
	l := e.launch(LaunchSpec{RunID: id, Command: "echo hello; echo oops 1>&2; exit 3", TimeoutSec: 30})
	if l.Err != "" || l.PID <= 0 || l.Shell == "" || l.Duplicate {
		t.Fatalf("启动结果异常: %+v", l)
	}
	r, out, errs := e.waitFinished(id, 10*time.Second)
	if *r.ExitCode != 3 || out != "hello\n" || errs != "oops\n" || r.Finished == 0 || r.PID != l.PID {
		t.Fatalf("结束状态异常: ec=%v out=%q err=%q fin=%d pid=%d/%d", *r.ExitCode, out, errs, r.Finished, r.PID, l.PID)
	}
	if r.Alive {
		t.Fatal("结束后进程不应存活")
	}
	// 任务目录权限必须是 700，命令文件 700。
	info, err := os.Stat(filepath.Join(e.srv.Home, ".relaymesh", "runs", id))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("任务目录权限应为 700: %v %v", info, err)
	}
}

func TestPollReturnsOnlyNewOutput(t *testing.T) {
	e := newJobEnv(t)
	id := newRunID(t)
	e.launch(LaunchSpec{RunID: id, Command: "echo first; sleep 1; echo second", TimeoutSec: 30})
	var got string
	var off int64
	deadline := time.Now().Add(8 * time.Second)
	sawRunning := false
	for time.Now().Before(deadline) {
		r := e.poll(id, off, 0)
		if r.ExitCode == nil && r.Alive {
			sawRunning = true
		}
		got += string(r.Out)
		off += int64(len(r.Out))
		if r.ExitCode != nil && off >= r.OutSize {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got != "first\nsecond\n" || !sawRunning {
		t.Fatalf("增量输出异常: %q running=%v", got, sawRunning)
	}
}

func TestLaunchIsIdempotent(t *testing.T) {
	e := newJobEnv(t)
	id := newRunID(t)
	sp := LaunchSpec{RunID: id, Command: `echo x >> "$HOME/count.txt"; sleep 1`, TimeoutSec: 30}
	first := e.launch(sp)
	second := e.launch(sp) // 连接中断后用新连接重放同一段脚本
	if !second.Duplicate || second.PID != first.PID {
		t.Fatalf("重放应识别为重复且不再启动: first=%+v second=%+v", first, second)
	}
	e.waitFinished(id, 10*time.Second)
	b, _ := os.ReadFile(filepath.Join(e.srv.Home, "count.txt"))
	if strings.Count(string(b), "x") != 1 {
		t.Fatalf("命令只应执行一次，实际 %q", b)
	}
}

func TestCommandTextIsNeverInterpreted(t *testing.T) {
	e := newJobEnv(t)
	id := newRunID(t)
	// 命令里充满了会破坏「拼接式」实现的内容：引号、反引号、命令替换、heredoc 终止符、非 ASCII、反斜杠。
	nasty := "printf '%s\\n' 'it'\"'\"'s' \"\\$HOME\" '$(touch \"$HOME/injected\")' '`touch \"$HOME/injected2\"`' 'RM_B64_EOF' 'RM_RUNNER_EOF' '中文 ✓' 'a\\\\b'\n" +
		"echo done"
	e.launch(LaunchSpec{RunID: id, Command: nasty, TimeoutSec: 30})
	_, out, errs := e.waitFinished(id, 10*time.Second)
	want := "it's\n$HOME\n$(touch \"$HOME/injected\")\n`touch \"$HOME/injected2\"`\nRM_B64_EOF\nRM_RUNNER_EOF\n中文 ✓\na\\\\b\ndone\n"
	if out != want || errs != "" {
		t.Fatalf("命令被改动或解释了:\n got %q\nwant %q\nerr %q", out, want, errs)
	}
	for _, f := range []string{"injected", "injected2"} {
		if _, err := os.Stat(filepath.Join(e.srv.Home, f)); err == nil {
			t.Fatalf("命令替换被执行了：生成了 %s", f)
		}
	}
	// 命令文本不应出现在 SSH 的命令行里（只经 stdin 传输）。
	for _, c := range e.srv.Commands() {
		if strings.Contains(c, "touch") || strings.Contains(c, "injected") || len(c) > 64 {
			t.Fatalf("SSH 命令行里不应出现用户命令: %q", c)
		}
	}
}

func TestLargeCommandRoundTrip(t *testing.T) {
	e := newJobEnv(t)
	id := newRunID(t)
	payload := strings.Repeat("0123456789abcdef", 1400) // 约 22 KiB，接近命令长度上限
	e.launch(LaunchSpec{RunID: id, Command: "printf '%s' '" + payload + "' | wc -c", TimeoutSec: 30})
	_, out, _ := e.waitFinished(id, 10*time.Second)
	if strings.TrimSpace(out) != "22400" {
		t.Fatalf("大命令传输损坏: %q", out)
	}
}

func TestWorkingDirectory(t *testing.T) {
	e := newJobEnv(t)
	dir := filepath.Join(e.srv.Home, "it's a dir with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := newRunID(t)
	e.launch(LaunchSpec{RunID: id, Command: "pwd", Cwd: dir, TimeoutSec: 30})
	_, out, _ := e.waitFinished(id, 10*time.Second)
	if strings.TrimSpace(out) != dir {
		t.Fatalf("工作目录不对: %q", out)
	}

	bad := newRunID(t)
	l := e.launch(LaunchSpec{RunID: bad, Command: "echo no", Cwd: filepath.Join(dir, "missing"), TimeoutSec: 30})
	if l.Err != LaunchErrCwd {
		t.Fatalf("不存在的目录应返回 %s，得到 %+v", LaunchErrCwd, l)
	}
	if _, err := os.Stat(filepath.Join(e.srv.Home, ".relaymesh", "runs", bad)); err == nil {
		t.Fatal("启动失败不应留下任务目录")
	}
}

// 没有指定工作目录时，命令应当在用户的家目录里运行（与 Agent 主机一致），而不是在保留 7 天后会被清理的任务目录里。
func TestDefaultWorkingDirectoryIsHome(t *testing.T) {
	e := newJobEnv(t)
	id := newRunID(t)
	e.launch(LaunchSpec{RunID: id, Command: "pwd; touch from-cmd.txt", TimeoutSec: 30})
	_, out, _ := e.waitFinished(id, 10*time.Second)
	if strings.TrimSpace(out) != e.srv.Home {
		t.Fatalf("默认工作目录应是家目录 %q，实际 %q", e.srv.Home, out)
	}
	if _, err := os.Stat(filepath.Join(e.srv.Home, "from-cmd.txt")); err != nil {
		t.Fatalf("相对路径的文件应落在家目录：%v", err)
	}
	if _, err := os.Stat(filepath.Join(e.srv.Home, ".relaymesh", "runs", id, "from-cmd.txt")); err == nil {
		t.Fatal("相对路径的文件不应落在任务目录里（任务目录会被定期清理）")
	}
}

func TestTimeoutWrapper(t *testing.T) {
	e := newJobEnv(t)
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("本机没有 timeout 命令")
	}
	id := newRunID(t)
	start := time.Now()
	e.launch(LaunchSpec{RunID: id, Command: "sleep 30", TimeoutSec: 1})
	r, _, _ := e.waitFinished(id, 10*time.Second)
	if *r.ExitCode != 124 || time.Since(start) > 6*time.Second {
		t.Fatalf("远端超时应以 124 结束且及时: ec=%d 用时 %v", *r.ExitCode, time.Since(start))
	}
}

// busybox 等实现的 timeout 超时后不返回 GNU 的 124，而是被终止的子进程的状态（TERM 为 143）。
// 运行脚本要把「已运行满超时时长、且是被信号结束」统一成 124；没到时间就被信号杀死的命令不能被误判成超时。
func TestTimeoutWrapperNormalizesBusyboxStyleStatus(t *testing.T) {
	dir := t.TempDir()
	fake := "#!/bin/sh\nt=$1; shift\n\"$@\" &\npid=$!\n( sleep \"$t\"; kill -TERM \"$pid\" 2>/dev/null ) &\nw=$!\nwait \"$pid\"\nrc=$?\nkill \"$w\" 2>/dev/null\nexit \"$rc\"\n"
	if err := os.WriteFile(filepath.Join(dir, "timeout"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	e := newJobEnv(t)

	id := newRunID(t)
	start := time.Now()
	e.launch(LaunchSpec{RunID: id, Command: "exec sleep 30", TimeoutSec: 1})
	r, _, _ := e.waitFinished(id, 10*time.Second)
	if *r.ExitCode != 124 || time.Since(start) > 8*time.Second {
		t.Fatalf("busybox 风格的超时应归一成 124 且及时: ec=%d 用时 %v", *r.ExitCode, time.Since(start))
	}

	// 没到超时就被信号结束：保持原样的 143，不是超时
	id2 := newRunID(t)
	e.launch(LaunchSpec{RunID: id2, Command: "kill -TERM $$", TimeoutSec: 30})
	r2, _, _ := e.waitFinished(id2, 10*time.Second)
	if *r2.ExitCode != 143 {
		t.Fatalf("提前被信号结束的命令应保持 143，实际 %d", *r2.ExitCode)
	}
}

func (e *jobEnv) cancel(id string) CancelResult {
	e.t.Helper()
	cmd, stdin, err := CancelCommand(id)
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := ParseCancel(e.run(cmd, stdin).Stdout)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// uniqSleep 返回本次测试进程独有的 sleep 秒数：避免上一次失败遗留的进程干扰计数。
func uniqSleep(offset int) int { return 7000 + os.Getpid()%1000*3 + offset }

func (e *jobEnv) countProcs(marker string) int {
	e.t.Helper()
	res := e.run("ps -eo args | grep -Ec '[s]leep "+marker+"$'", nil)
	n := strings.TrimSpace(string(res.Stdout))
	if n == "" {
		return 0
	}
	var c int
	for _, ch := range n {
		c = c*10 + int(ch-'0')
	}
	return c
}

func TestCancelKillsTheWholeProcessGroup(t *testing.T) {
	e := newJobEnv(t)
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("需要 setsid")
	}
	id := newRunID(t)
	a, b := uniqSleep(0), uniqSleep(1)
	pat := fmt.Sprintf("(%d|%d)", a, b)
	e.launch(LaunchSpec{RunID: id, Command: fmt.Sprintf("sleep %d & sleep %d & wait", a, b), TimeoutSec: 600})
	deadline := time.Now().Add(5 * time.Second)
	for e.countProcs(pat) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("子进程没有按预期启动")
		}
		time.Sleep(50 * time.Millisecond)
	}
	r := e.cancel(id)
	if !r.Termed && !r.Killed {
		t.Fatalf("取消结果异常: %+v", r)
	}
	time.Sleep(300 * time.Millisecond)
	if n := e.countProcs(pat); n != 0 {
		t.Fatalf("取消后进程组里还剩 %d 个子进程", n)
	}
	if p := e.poll(id, 0, 0); p.Alive {
		t.Fatalf("取消后运行器不应存活: %+v", p)
	}
}

func TestCancelEscalatesToKill(t *testing.T) {
	e := newJobEnv(t)
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("需要 setsid")
	}
	id := newRunID(t)
	n := uniqSleep(2)
	e.launch(LaunchSpec{RunID: id, Command: fmt.Sprintf("trap '' TERM; while true; do sleep %d; done", n), Shell: "sh", TimeoutSec: 600})
	time.Sleep(400 * time.Millisecond)
	start := time.Now()
	r := e.cancel(id)
	if !r.Killed {
		t.Fatalf("忽略 TERM 的命令应被 KILL: %+v", r)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("升级到 KILL 不应太慢: %v", time.Since(start))
	}
	time.Sleep(300 * time.Millisecond)
	if left := e.countProcs(fmt.Sprint(n)); left != 0 {
		t.Fatalf("KILL 之后仍有 %d 个残留进程", left)
	}
}

func TestCancelAfterFinishAndUnknown(t *testing.T) {
	e := newJobEnv(t)
	id := newRunID(t)
	e.launch(LaunchSpec{RunID: id, Command: "true", TimeoutSec: 30})
	e.waitFinished(id, 10*time.Second)
	if r := e.cancel(id); !r.Done {
		t.Fatalf("已结束的运行取消应报告 Done: %+v", r)
	}
	if r := e.cancel(newRunID(t)); !r.Gone {
		t.Fatalf("不存在的运行取消应报告 Gone: %+v", r)
	}
	if p := e.poll(newRunID(t), 0, 0); !p.Gone {
		t.Fatalf("不存在的运行轮询应报告 Gone: %+v", p)
	}
}

func TestReadPages(t *testing.T) {
	e := newJobEnv(t)
	id := newRunID(t)
	e.launch(LaunchSpec{RunID: id, Command: "printf 0123456789; printf ERRDATA 1>&2", TimeoutSec: 30})
	e.waitFinished(id, 10*time.Second)
	read := func(stream string, off int64, max int) ReadResult {
		t.Helper()
		cmd, stdin, err := ReadCommand(ReadSpec{RunID: id, Stream: stream, Off: off, Max: max})
		if err != nil {
			t.Fatal(err)
		}
		r, err := ParseRead(e.run(cmd, stdin).Stdout)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := read("stdout", 3, 4); string(r.Data) != "3456" || r.Size != 10 {
		t.Fatalf("分页读取异常: %+v", r)
	}
	if r := read("stdout", 0, 0); string(r.Data) != "0123456789" {
		t.Fatalf("整段读取异常: %+v", r)
	}
	if r := read("stdout", 10, 5); len(r.Data) != 0 || r.Size != 10 {
		t.Fatalf("越过末尾应返回空数据: %+v", r)
	}
	if r := read("stderr", 0, 100); string(r.Data) != "ERRDATA" {
		t.Fatalf("stderr 读取异常: %+v", r)
	}
	cmd, stdin, _ := ReadCommand(ReadSpec{RunID: newRunID(t), Stream: "stdout"})
	if r, _ := ParseRead(e.run(cmd, stdin).Stdout); !r.Gone {
		t.Fatalf("不存在的运行应报告 Gone: %+v", r)
	}
}

func TestBinaryOutputSurvivesTransport(t *testing.T) {
	e := newJobEnv(t)
	id := newRunID(t)
	e.launch(LaunchSpec{RunID: id, Command: `printf '\000\001\377\376line1\nline2\r\n\200'`, TimeoutSec: 30})
	_, out, _ := e.waitFinished(id, 10*time.Second)
	if out != "\x00\x01\xff\xfeline1\nline2\r\n\x80" {
		t.Fatalf("二进制输出被破坏: %q", out)
	}
}

func TestSweepRemovesOnlyOldFinishedRuns(t *testing.T) {
	e := newJobEnv(t)
	old := newRunID(t)
	e.launch(LaunchSpec{RunID: old, Command: "true", TimeoutSec: 30})
	e.waitFinished(old, 10*time.Second)
	running := newRunID(t)
	e.launch(LaunchSpec{RunID: running, Command: fmt.Sprintf("sleep %d", uniqSleep(0)), TimeoutSec: 600})
	fresh := newRunID(t)
	e.launch(LaunchSpec{RunID: fresh, Command: "true", TimeoutSec: 30})
	e.waitFinished(fresh, 10*time.Second)

	base := filepath.Join(e.srv.Home, ".relaymesh", "runs")
	past := time.Now().Add(-10 * 24 * time.Hour)
	for _, id := range []string{old, running} {
		if err := os.Chtimes(filepath.Join(base, id), past, past); err != nil {
			t.Fatal(err)
		}
	}
	// 一个名字不是 32 位十六进制的目录不属于我们，绝不能动。
	stranger := filepath.Join(base, "not-ours")
	if err := os.MkdirAll(stranger, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(stranger, past, past)

	cmd, stdin, err := SweepCommand(7)
	if err != nil {
		t.Fatal(err)
	}
	res := e.run(cmd, stdin)
	if !strings.Contains(string(res.Stdout), "RM_SWEPT 1") {
		t.Fatalf("应只清理 1 个目录: %q", res.Stdout)
	}
	if _, err := os.Stat(filepath.Join(base, old)); err == nil {
		t.Fatal("过期的已结束任务应被清理")
	}
	for _, keep := range []string{running, fresh, "not-ours"} {
		if _, err := os.Stat(filepath.Join(base, keep)); err != nil {
			t.Fatalf("%s 不应被清理: %v", keep, err)
		}
	}
	e.cancel(running)
}

func TestFactsAndUnsupportedHosts(t *testing.T) {
	e := newJobEnv(t)
	cmd, stdin := FactsCommand()
	f, err := ParseFacts(e.run(cmd, stdin).Stdout)
	if err != nil || f.OS == "" || f.Home != e.srv.Home || !f.B64 {
		t.Fatalf("探测结果异常: %+v %v", f, err)
	}

	win := sshdtest.Start(t, sshdtest.Options{Password: testPassword, Handler: func(string, []byte) ([]byte, []byte, int, bool) {
		return nil, []byte("'sh' is not recognized as an internal or external command"), 1, true
	}})
	spec := passwordSpec(win, "tester")
	res, err := (&Pool{DialTimeout: 3 * time.Second}).Exec(context.Background(), "w", spec, cmd, stdin, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFacts(res.Stdout); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("非 POSIX 主机应返回 ErrUnsupported，得到 %v", err)
	}
}

func TestScriptBuildersRejectUnsafeInput(t *testing.T) {
	good := newRunID(t)
	for _, id := range []string{"", "../../etc", good + "x", strings.ToUpper(good), good[:31], "a b", good + "\n", "$(id)", "0123456789abcdef0123456789abcde;"} {
		if ValidRunID(id) {
			t.Errorf("%q 不应是合法运行 ID", id)
		}
		if _, _, err := LaunchCommand(LaunchSpec{RunID: id, Command: "true"}); err == nil {
			t.Errorf("LaunchCommand 应拒绝 %q", id)
		}
		if _, _, err := PollCommand(PollSpec{RunID: id}); err == nil {
			t.Errorf("PollCommand 应拒绝 %q", id)
		}
		if _, _, err := CancelCommand(id); err == nil {
			t.Errorf("CancelCommand 应拒绝 %q", id)
		}
		if _, _, err := ReadCommand(ReadSpec{RunID: id, Stream: "stdout"}); err == nil {
			t.Errorf("ReadCommand 应拒绝 %q", id)
		}
	}
	for _, sh := range []string{"pwsh", "powershell", "cmd", "bash; id", "/bin/sh", "BASH"} {
		if _, _, err := LaunchCommand(LaunchSpec{RunID: good, Command: "true", Shell: sh}); err == nil {
			t.Errorf("LaunchCommand 应拒绝 shell %q", sh)
		}
	}
	for _, sh := range []string{"", "bash", "sh", "zsh"} {
		if _, _, err := LaunchCommand(LaunchSpec{RunID: good, Command: "true", Shell: sh}); err != nil {
			t.Errorf("shell %q 应被接受: %v", sh, err)
		}
	}
	if _, _, err := LaunchCommand(LaunchSpec{RunID: good, Command: "true", TimeoutSec: -1}); err == nil {
		t.Error("负超时应被拒绝")
	}
	if _, _, err := PollCommand(PollSpec{RunID: good, OutOff: -1}); err == nil {
		t.Error("负偏移应被拒绝")
	}
	if _, _, err := ReadCommand(ReadSpec{RunID: good, Stream: "stdin"}); err == nil {
		t.Error("未知流应被拒绝")
	}
	if _, _, err := SweepCommand(0); err == nil {
		t.Error("0 天应被拒绝")
	}
	// 脚本里不应残留未替换的占位符。
	cmd, stdin, _ := LaunchCommand(LaunchSpec{RunID: good, Command: "true", Cwd: "/tmp", TimeoutSec: 5})
	_ = cmd
	if strings.Contains(string(stdin), "@RUN@") || strings.Contains(string(stdin), "@CMD@") || strings.Contains(string(stdin), "@CWD@") {
		t.Error("启动脚本里残留了占位符")
	}
}

func TestParsersAreStrict(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("data"))
	// 登录脚本可能在协议行前后混入别的内容：应被忽略。
	noisy := "welcome banner\nRM_ST pid=12 alive=1 ec=- fin=- out=4 err=0\nRM_DATA_OUT\n" + b64 + "\nRM_END\ntrailing junk\n"
	r, err := ParsePoll([]byte(noisy))
	if err != nil || string(r.Out) != "data" || r.PID != 12 || !r.Alive || r.ExitCode != nil || r.OutSize != 4 {
		t.Fatalf("带噪声的输出应能解析: %+v %v", r, err)
	}
	r, err = ParsePoll([]byte("RM_ST pid=1 alive=0 ec=130 fin=1700000000 out=0 err=0\n"))
	if err != nil || r.ExitCode == nil || *r.ExitCode != 130 || r.Finished != 1700000000 {
		t.Fatalf("退出码解析异常: %+v %v", r, err)
	}
	bad := map[string]string{
		"没有状态行":     "hello\n",
		"退出码越界":     "RM_ST pid=1 alive=0 ec=999 fin=- out=0 err=0\n",
		"退出码不是数字":   "RM_ST pid=1 alive=0 ec=x fin=- out=0 err=0\n",
		"base64 非法": "RM_ST pid=1 alive=1 ec=- fin=- out=1 err=0\nRM_DATA_OUT\n@@@\nRM_END\n",
		"数据段被截断":    "RM_ST pid=1 alive=1 ec=- fin=- out=1 err=0\nRM_DATA_OUT\n",
		"缺少结束标记":    "RM_ST pid=1 alive=1 ec=- fin=- out=1 err=0\nRM_DATA_OUT\n" + b64 + "\nRM_ST pid=2\n",
	}
	for name, in := range bad {
		if _, err := ParsePoll([]byte(in)); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s：应返回 ErrProtocol，得到 %v", name, err)
		}
	}
	if _, err := ParseLaunch([]byte("nothing useful\n")); !errors.Is(err, ErrProtocol) {
		t.Errorf("启动输出缺少进程号应返回 ErrProtocol，得到 %v", err)
	}
	if _, err := ParseCancel([]byte("")); !errors.Is(err, ErrProtocol) {
		t.Errorf("取消输出为空应返回 ErrProtocol，得到 %v", err)
	}
	if l, err := ParseLaunch([]byte("RM_ERR cwd\n")); err != nil || l.Err != "cwd" {
		t.Errorf("启动失败应能解析: %+v %v", l, err)
	}
}
