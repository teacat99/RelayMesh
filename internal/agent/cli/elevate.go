package cli

import (
	"bufio"
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
)

// 本文件是 Windows「临时启动」体验的平台无关逻辑：
//   - 未提权的交互式 `run --temp` 会请求提升（UAC），由提权后的子进程完成握手，父进程只等待、不发起任何连接；
//   - 无参数双击 exe 时进入向导，问清服务端地址后以临时身份运行；
//   - 独占控制台窗口（双击启动 / 提权子进程）退出前暂停，避免错误信息一闪而过。
// 真实的系统调用在 elevate_windows.go，这里通过函数变量注入，方便在任何平台上测试。

// goos 与三个系统钩子在测试里替换。
var (
	goos = runtime.GOOS
	// relaunchElevated 以管理员身份重新启动当前程序并等待其退出，返回子进程退出码。
	// 用户在 UAC 里选择「否」时返回 errElevationDeclined。
	relaunchElevated = relaunchElevatedImpl
	// ownsConsole 报告控制台窗口是否只属于当前进程（双击启动时为真；从已有终端启动时为假）。
	ownsConsole = ownsConsoleImpl
	isElevated  = agent.IsElevated
)

// errElevationDeclined 表示用户拒绝了 UAC 提示。
var errElevationDeclined = errors.New("用户拒绝了管理员权限请求")

const envNoElevate = "RELAYMESH_AGENT_NO_ELEVATE"

type elevationInput struct {
	goos        string
	temp        bool
	elevated    bool
	interactive bool // stdin 是终端，且日志没有被重定向到文件
	noElevate   bool
	relaunched  bool
}

// shouldElevate 决定是否请求提升。条件刻意收得很紧：
//   - 只在 Windows 的临时身份运行时提升：持久身份的位置随权限而变（用户目录 / ProgramData），提升后机器码会变，令人困惑；
//   - 必须是交互式前台：计划任务、服务、脚本、CI 都没有人去点 UAC；
//   - 已提升、已显式拒绝、或本身就是提升后的子进程（防止无限重启）都不再提升。
func shouldElevate(in elevationInput) bool {
	return in.goos == "windows" && in.temp && !in.elevated && in.interactive && !in.noElevate && !in.relaunched
}

// relaunchArgs 把已解析的配置还原成显式参数交给提权后的子进程。
// 不能依赖环境变量：UAC 提升后的进程环境不一定与当前一致。
func relaunchArgs(env Env, c common) []string {
	args := append([]string(nil), env.ServiceArgs...)
	args = append(args, "run", "--temp", "--server", c.server)
	for _, p := range c.pins {
		args = append(args, "--pin", p)
	}
	if c.insecure {
		args = append(args, "--insecure")
	}
	if c.noTOFU {
		args = append(args, "--no-tofu")
	}
	if c.maxSessions > 0 {
		args = append(args, "--max-sessions", strconv.Itoa(c.maxSessions))
	}
	if c.configDir != "" {
		args = append(args, "--config-dir", c.configDir)
	}
	return append(args, "--relaunched")
}

// maybeElevate 在需要时请求提升并在提权子进程里完成运行；handled 为真表示本进程无需再运行，code 是应返回的退出码。
func maybeElevate(env Env, c common, interactive bool) (code int, handled bool) {
	if !shouldElevate(elevationInput{
		goos:        goos,
		temp:        c.temp,
		elevated:    isElevated(),
		interactive: interactive,
		noElevate:   c.noElevate,
		relaunched:  c.relaunched,
	}) {
		return 0, false
	}
	env.printf("当前不是管理员权限。为了让终端拥有管理员身份，将请求提升权限（会弹出 Windows 的 UAC 确认）。\n")
	env.printf("  · 在弹窗里选「是」：Agent 会在新的管理员窗口里运行，本窗口等它退出；选「否」则以当前权限继续。\n")
	env.printf("  · 不需要管理员权限？加 --no-elevate（或设置环境变量 %s=1）即可跳过。\n\n", envNoElevate)

	code, err := relaunchElevated(env, relaunchArgs(env, c))
	switch {
	case err == nil:
		env.printf("管理员窗口中的 Agent 已退出（退出码 %d）。\n", code)
		return code, true
	case errors.Is(err, errElevationDeclined):
		env.printf("已取消提升权限，将以当前权限继续运行。\n\n")
	default:
		env.errorf("无法请求管理员权限（%v），将以当前权限继续运行。\n\n", err)
	}
	return 0, false
}

// pauseBeforeClose 在独占的控制台窗口里等待回车再退出：双击启动或提权子进程的窗口，退出即消失，错误信息会一闪而过。
func pauseBeforeClose(env Env) {
	if !isInteractive(env.Stdin) || !ownsConsole() {
		return
	}
	env.printf("\n按回车键关闭窗口…")
	_, _ = bufio.NewReader(env.Stdin).ReadString('\n')
}

// ---------- 无参数双击：向导 ----------

func wizardAvailable(env Env) bool { return goos == "windows" && isInteractive(env.Stdin) }

// runWizard 询问服务端地址与证书信任方式，返回交给 run 的参数。用户取消（空输入）时 ok 为假。
func runWizard(env Env) (args []string, ok bool) {
	in := bufio.NewReader(env.Stdin)
	ask := func(prompt string) (string, bool) {
		env.printf("%s", prompt)
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if err != nil && line == "" {
			return "", false
		}
		return line, true
	}

	env.printf("RelayMesh Agent %s —— 让 RelayMesh 的 Web 终端连接这台主机\n\n", env.Version)
	env.printf("没有指定服务端地址，进入临时连接向导：身份只保存在内存里，关闭窗口后机器码失效。\n")
	env.printf("（想长期接入请用 install；留空直接回车可退出。）\n\n")

	var server string
	for attempt := 0; attempt < 3; attempt++ {
		line, more := ask("RelayMesh 服务端地址（例如 https://relay.example.com）: ")
		if !more || line == "" {
			env.printf("已取消。\n")
			return nil, false
		}
		if !strings.Contains(line, "://") {
			line = "https://" + line
		}
		if _, err := agent.ParseServer(line); err != nil {
			env.printf("  地址不合法：%v\n", err)
			continue
		}
		server = line
		break
	}
	if server == "" {
		env.printf("多次输入不合法，已退出。\n")
		return nil, false
	}

	args = []string{"--temp", "--server", server}
	target, _ := agent.ParseServer(server)
	if !target.Secure {
		ans, _ := ask("这是明文连接：数据不加密，也不校验服务端身份。确认只在你完全可控的内网使用？[y/N]: ")
		if a := strings.ToLower(ans); a != "y" && a != "yes" {
			env.printf("已取消。请改用 https:// 地址。\n")
			return nil, false
		}
		return append(args, "--insecure"), true
	}
	pin, _ := ask("服务端使用自签名证书吗？粘贴 Web 面板给出的指纹（sha256:…）；使用受信任证书就直接回车: ")
	if pin != "" {
		if !pinPattern(pin) {
			env.printf("  指纹格式不对（应为 sha256: 加 64 位十六进制），已忽略；未知证书时会询问是否信任。\n")
		} else {
			args = append(args, "--pin", pin)
		}
	}
	return args, true
}

func pinPattern(s string) bool {
	h, ok := strings.CutPrefix(strings.ToLower(s), "sha256:")
	if !ok || len(h) != 64 {
		return false
	}
	for _, r := range h {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// runNoArgs 处理「没有参数、也没有环境变量配置」的启动：Windows 上的交互式终端（典型是双击 exe）进入向导，其余显示用法。
func runNoArgs(ctx context.Context, env Env) int {
	if !wizardAvailable(env) {
		usage(env)
		return 2
	}
	args, ok := runWizard(env)
	if !ok {
		pauseBeforeClose(env)
		return 2
	}
	env.printf("\n")
	code := cmdRun(ctx, args, env)
	pauseBeforeClose(env)
	return code
}
