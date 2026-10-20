// Package cli 是终端 Agent 的命令行：独立二进制 relaymesh-agent 与主程序的 `relaymesh agent` 子命令共用同一份实现。
//
// 退出码约定：0 成功；1 运行期失败；2 用法错误。
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// Env 是命令行的运行环境。
type Env struct {
	// Version 是 Agent 版本号。
	Version string
	// Rev 是 Agent 二进制的「Agent 侧代码指纹」（见 termproto.ValidAgentRev）；空表示非发布构建。
	// 它决定「要不要升级 / 守护进程要不要重启」：指纹一致说明两边的 Agent 代码相同，版本号不同只是构建号不同。
	Rev string
	// Prog 是帮助与提示里出现的命令名，例如 "relaymesh-agent" 或 "relaymesh agent"。
	Prog string
	// ServiceArgs 是服务单元里跟在可执行文件之后的前缀参数：独立二进制为空，主程序子命令为 ["agent"]。
	ServiceArgs []string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Getenv 读取环境变量；为 nil 时使用 os.Getenv（测试注入）。
	Getenv func(string) string
}

func (e *Env) normalize() {
	if e.Stdin == nil {
		e.Stdin = os.Stdin
	}
	if e.Stdout == nil {
		e.Stdout = os.Stdout
	}
	if e.Stderr == nil {
		e.Stderr = os.Stderr
	}
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	if e.Prog == "" {
		e.Prog = "relaymesh-agent"
	}
	if e.Version == "" {
		e.Version = "dev"
	}
}

func (e Env) printf(format string, args ...any) { fmt.Fprintf(e.Stdout, format, args...) }
func (e Env) errorf(format string, args ...any) { fmt.Fprintf(e.Stderr, format, args...) }

// Run 解析并执行命令，返回进程退出码。
func Run(ctx context.Context, args []string, env Env) int {
	env.normalize()

	if len(args) == 0 {
		// 只配了环境变量（容器 / 服务管理器常见）：直接运行。
		if env.Getenv(envServer) != "" {
			return cmdRun(ctx, nil, env)
		}
		// Windows 上双击 exe：没有参数，进入向导而不是一闪而过的用法说明。
		return runNoArgs(ctx, env)
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(ctx, rest, env)
	case "install":
		return cmdInstall(ctx, rest, env)
	case "uninstall":
		return cmdUninstall(ctx, rest, env)
	case "upgrade":
		return cmdUpgrade(ctx, rest, env)
	case "web-upgrade":
		return cmdWebUpgrade(ctx, rest, env)
	case "auto-upgrade":
		return cmdAutoUpgrade(ctx, rest, env)
	case "status":
		return cmdStatus(ctx, rest, env)
	case "holder":
		return cmdHolder(ctx, rest, env)
	case "id":
		return cmdID(rest, env)
	case "reset-id":
		return cmdResetID(rest, env)
	case "version", "--version", "-v":
		env.printf("RelayMesh Agent %s\n", env.Version)
		return 0
	case "help", "--help", "-h":
		usage(env)
		return 0
	}
	if strings.HasPrefix(cmd, "-") {
		// `relaymesh-agent --server …` 等价于 `relaymesh-agent run --server …`。
		return cmdRun(ctx, args, env)
	}
	env.errorf("未知命令 %q\n\n", cmd)
	usage(env)
	return 2
}

func usage(env Env) {
	env.printf(`RelayMesh Agent %[2]s —— 让 RelayMesh 的 Web 终端连接这台主机

用法:
  %[1]s <命令> [选项]

命令:
  run         前台运行（按 Ctrl+C 退出；没有会话守护进程时终端会话随 Agent 进程结束）
  install     安装为常驻服务（Linux systemd / macOS launchd / Windows 计划任务），开机或登录后自启；
              默认同时安装「会话守护进程」，让终端会话挺过 Agent 的升级 / 重启 / 崩溃
  upgrade     用当前这个（新）程序替换已安装的常驻服务并只重启 Agent：服务配置不改，会话守护进程里的会话不受影响，机器码与启用状态不变（只认 --user）
  web-upgrade 决定是否允许管理员在 Web 上一键升级这台主机的 Agent：on / off / status（默认不允许；只有这台主机的所有者能打开，服务端改不了；仅 Linux 的 systemd 常驻服务）
  auto-upgrade 决定这台主机的 Agent 要不要自己留意新版本：enable --mode notify|silent [--window HH:MM-HH:MM] [--trust-key rmpub1:…] / disable / status / keys（列出这个程序编进去的可信发布公钥）（默认关闭；只有这台主机的所有者能开，服务端改不了；静默模式要验发布签名；仅 Linux 的 systemd 常驻服务）
  uninstall   卸载常驻服务（默认保留机器码，--purge 一并删除）
  status      显示本机机器码与服务状态
  holder      管理会话守护进程（status / restart / stop / run），详见 %[1]s holder --help
  id          只打印本机机器码
  reset-id    重新生成机器码（旧机器码需要在 Web 上另行清理）
  version     显示版本

常用选项（run / install）:
  --server <地址>       RelayMesh 服务端，例如 https://relay.example.com（也可用环境变量 %[3]s）
  --pin sha256:<指纹>   固定服务端证书指纹（自签名证书时使用，可重复；Web「接入主机」面板会给出完整命令）
  --insecure            允许明文 ws://（仅限你完全控制的内网）
  --temp                使用临时身份（不落盘，每次运行机器码都不同；仅 run）
  --persist             显式使用持久身份（run 的默认行为；与 --temp 互斥）
  --no-elevate          Windows：临时运行时不请求管理员权限（默认在交互式终端里请求 UAC，也可用环境变量 %[5]s）
  --data-dir <目录>     身份文件目录（默认按用户 / 系统区分）
  --holder <模式>       终端会话由谁持有：auto（run 的默认：有守护进程就交给它，否则内嵌）/ external / embedded（install 的 embedded 只装一个服务）
  --holder-socket <路径> 会话守护进程的本地套接字（默认在数据目录下）
  --no-record           本机一票否决：不录制终端会话（终端上显示什么就记录什么，只存在本机；服务端无法覆盖，也可用环境变量 %[6]s）
  --record-session-mb / --record-host-mb / --record-retention-days / --record-min-free-mb
                        记录的单会话上限（默认 64 MiB）/ 整机上限（512 MiB）/ 会话结束后保留天数（30）/ 剩余磁盘低于多少停止录制（1024 MiB）
  --deny-model-exec     本机一票否决：禁止模型在本机执行命令（服务端无法覆盖；人工终端不受影响，也可用环境变量 %[7]s）
  --no-ssh-gateway      关闭 SSH 网关（也可写 --ssh-gateway=false，或设环境变量 %[8]s=off）。网关与主机服务一样默认开启：服务端可以借这台主机去连接没有安装 Agent 的 SSH 主机，也让模型经它在这些主机上执行命令；临时身份（--temp）不提供网关
  --ssh-allow <目标>    SSH 网关只允许连接这些目标：CIDR（10.0.0.0/8）、IP 或主机名（可含 * 通配，可重复；也可用环境变量 %[9]s）。这是网关能连到哪里的唯一边界
  --user                仅为当前用户安装服务（install / uninstall / status；Windows 上不需要管理员）
  --no-linger           Linux：--user 安装时不要自动执行 loginctl enable-linger（默认会开启，让服务在退出登录后继续运行；卸载时只关闭由安装器开启的）
  --auto-upgrade <模式> install：装好后同时开启自动升级——notify（只提示新版本）/ silent（在允许的时间段内自己下载、验签、升级）；仅 Linux 的 systemd 常驻服务，之后可用 auto-upgrade 命令修改或收回
  --auto-window HH:MM-HH:MM  配合 --auto-upgrade silent：允许开始升级的时间段（主机本地时间，可跨午夜；默认不限）
  --highest             Windows：与 --user 合用，当前用户身份 + 最高权限（登录时自动带管理员令牌，无 UAC 弹窗；创建 / 修改 / 卸载任务需要管理员 PowerShell）
  --log-file <文件>     日志写入文件而不是终端（自动轮转；Windows 常驻任务默认启用，也可用环境变量 %[4]s）

示例:
  %[1]s run --server https://relay.example.com
  sudo %[1]s install --server https://relay.example.com --pin sha256:...   （Linux / macOS；已是 root 或在 Windows 管理员 PowerShell 里去掉 sudo）
  %[1]s install --user --server https://relay.example.com                  （仅当前用户，不需要 root / 管理员；Linux 上会自动开启 linger）
  %[1]s install --user --highest --server https://relay.example.com        （Windows：当前用户 + 最高权限，管理员 PowerShell）
  sudo %[1]s install --server https://relay.example.com --auto-upgrade silent --auto-window 03:00-05:00   （Linux：装好后同时开启静默自动升级）
  %[1]s id

Windows 双击 exe（不带任何参数）会进入向导：问清服务端地址后以临时身份运行，关闭窗口即退出。

说明:
  首次连接时，Agent 会在 Web 端的「接入主机」里出现；请核对机器码后再在网页上启用。
  Agent 不保存任何可冒充主机的共享秘密：身份是本机生成的密钥，服务端只保存公钥。
  终端内容的记录只写在本机数据目录的 records/ 下（权限仅限当前用户），服务端不保存任何终端内容；
  它记录的是终端上显示出来的一切（包括 shell 对键入内容的回显），没有回显的口令输入不会出现；
  在 Web 上创建会话时可以选择「无痕」，该会话不留任何记录。
  模型执行（模型通过 MCP 在本机运行命令）默认需要服务端的多道授权同时放行；本机只有一个开关 --deny-model-exec，
  设置后无论服务端怎么授权，命令都不会在本机运行。命令的完整输出只保存在本机数据目录的 runs/ 下。
  SSH 网关（默认开启，不分版本；--no-ssh-gateway 关闭）让这台主机可以充当内网 SSH 目标的出站入口：在 Web 上登记目标时，登录凭据只交给网关一次，由它真实登录验证后
  密封保存在本机数据目录的 ssh-gateway/ 下（服务端不保存任何副本）；此后由它出站连接目标、持有终端会话，并代为执行模型发起的命令，
  目标不必能被服务端直接访问。目标的主机密钥必须已在 Web 上核对固定；网关能连到哪些目标由 --ssh-allow 限定，--deny-model-exec 同样管到经它接入的主机。
  凭据库的密封密钥默认是数据目录里自动生成的文件；也可以用环境变量 %[10]s 外置（至少 16 个字符），外置后凭据库文件单独被拷走也无法解开。
`, env.Prog, env.Version, envServer, envLogFile, envNoElevate, envNoRecord, envDenyModelExec, envSSHGateway, envSSHAllow, envSSHCredKey)
}

// confirm 在交互式终端里询问 yes/no；非交互场景（stdin 不是终端）一律视为「否」。
func confirm(env Env, prompt string) bool {
	if !isInteractive(env.Stdin) {
		return false
	}
	env.printf("%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "是":
		return true
	}
	return false
}

// isInteractive 判断 r 是否是一个终端。用变量是为了让测试注入。
var isInteractive = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && isTerminalFile(f)
}
