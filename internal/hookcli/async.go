package hookcli

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 分离子进程的异步上报（设计稿 04 §2.5，#113）。
//
// 不需要回包的 hook 事件（会话开始 / 结束、提交提示词、思考、工具开始、回复、工具失败、压缩）如果在 hook 进程里同步发 HTTP，
// 每次都会拖慢 Cursor（beforeSubmitPrompt / preToolUse 还是阻塞式的）。所以父进程只做三件事：读载荷、按白名单生成上报、
// 起一个分离出去的子进程（relaymesh-hook report）把上报写进它的标准输入，然后立刻返回——父进程不等子进程。
// 子进程自己找配置、解析令牌、发 HTTP（最长 2 秒）、退出；令牌不经过命令行，也不经过标准输入。
//
// 保护措施：
//   - 本地节流：思考事件很密，同一对话 5 秒内最多起一个子进程；
//   - 熔断：子进程发现 Hub 连不上（或返回错误）就留下一个 30 秒的熔断标记，期间父进程不再起子进程，免得 Hub 挂了时满地都是等超时的进程；
//   - 子进程不继承邮箱环境变量（Cursor 会导出它，我们既不读也不往下传）。

const (
	// reportTimeout 是子进程整次上报的上限。
	reportTimeout = 2 * time.Second
	// breakerHold 是上报失败后暂停起子进程的时间。
	breakerHold = 30 * time.Second
)

// reportJob 是父进程交给子进程的任务：配置文件的路径（不含任何机密）与已经清洗过的上报。
type reportJob struct {
	ConfigPath string         `json:"config_path"`
	Report     map[string]any `json:"report"`
}

// spawnReport 把一次上报交给分离出去的子进程。变量是为了让测试换成记录器：
// 测试二进制不能当 relaymesh-hook 来 exec（它会把整套测试再跑一遍）。
var spawnReport = spawnDetached

func spawnDetached(job reportJob) error {
	exe, err := selfBinary()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "report")
	cmd.Dir = os.TempDir()
	cmd.Env = childEnv(os.Environ())
	cmd.SysProcAttr = detachAttr()
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// 任务只有几百字节，远小于管道缓冲，写入不会阻塞；写完立刻关掉，子进程读到 EOF 就开始干活。
	_, werr := in.Write(payload)
	_ = in.Close()
	_ = cmd.Process.Release()
	return werr
}

// childEnv 生成子进程的环境：原样继承（令牌、HOME 都要靠它），只去掉不该往下传的邮箱变量。
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "CURSOR_USER_EMAIL=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// RunReport 是子进程的入口：从标准输入读一个任务，把上报发给 Hub。任何异常都静默退出。
func RunReport(stdin []byte) int {
	defer func() { _ = recover() }()
	var job reportJob
	if json.Unmarshal(stdin, &job) != nil || strings.TrimSpace(job.ConfigPath) == "" {
		return 0
	}
	report := sanitizeReport(job.Report)
	if report == nil {
		return 0
	}
	cfg := loadJSON(job.ConfigPath)
	if cfg["enabled"] == false {
		return 0
	}
	workflow := strings.TrimSpace(str(cfg["workflow_id"]))
	ep := resolveEndpoint(cfg)
	if workflow == "" || ep.URL == "" || ep.Source == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	if _, err := peek(ctx, ep, workflow, false, "hook", report); err != nil {
		markReportFailure(workflow, ep.URL)
		return 0
	}
	clearReportFailure(workflow, ep.URL)
	return 0
}

// ---- 本地节流与熔断（状态都放在临时目录，尽力而为，丢了不影响正确性） ----

func stateKey(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

func breakerPath(workflow, url string) string {
	return filepath.Join(os.TempDir(), "relaymesh-ev-"+stateKey(workflow, normURL(maskURL(url)))+".fail")
}

// breakerOpen 报告熔断是否生效：最近一次上报失败后的 30 秒内不再起子进程。
func breakerOpen(workflow, url string) bool {
	b, err := os.ReadFile(breakerPath(workflow, url))
	if err != nil {
		return false
	}
	until, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return err == nil && time.Now().Unix() < until
}

func markReportFailure(workflow, url string) {
	path := breakerPath(workflow, url)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if os.WriteFile(tmp, []byte(strconv.FormatInt(time.Now().Add(breakerHold).Unix(), 10)), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func clearReportFailure(workflow, url string) {
	_ = os.Remove(breakerPath(workflow, url))
}

// eventThrottleOK 判断同一对话里的这类事件是不是离上一次上报已经够久；够久就记下这次并返回 true。
// 所有事件共用一个状态文件（每个工作流 + 地址一个），只保留最近的一小批记录。
func eventThrottleOK(workflow, url, conversation, wire string, min time.Duration) bool {
	if min <= 0 {
		return true
	}
	path := filepath.Join(os.TempDir(), "relaymesh-ev-"+stateKey(workflow, normURL(maskURL(url)))+".json")
	key := conversation + "|" + wire
	now := time.Now().UnixMilli()
	state := map[string]int64{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &state)
	}
	if last, ok := state[key]; ok && now-last >= 0 && now-last < min.Milliseconds() {
		return false
	}
	state[key] = now
	// 只留最近 1 小时内的记录，且不超过 64 条。
	for k, t := range state {
		if now-t > int64(time.Hour/time.Millisecond) || now-t < 0 {
			delete(state, k)
		}
	}
	for len(state) > 64 {
		var oldestKey string
		var oldest int64 = 1<<62 - 1
		for k, t := range state {
			if t < oldest {
				oldestKey, oldest = k, t
			}
		}
		delete(state, oldestKey)
	}
	raw, _ := json.Marshal(state)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
	return true
}
