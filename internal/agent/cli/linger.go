package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// 用户级 systemd 服务默认只在该用户有登录会话时运行：最后一个会话退出（注销、ssh 断开）后，
// 用户管理器随之停止，服务也就停了。开启 linger 后，用户管理器在开机时启动、注销后继续存活，
// 用户级服务才真正常驻。
//
// 所以「常驻的 Linux 用户级安装」顺手把 linger 打开，使用者不必再记一条额外的命令。
// 系统级服务（本来就不依赖登录）、前台运行与临时运行（run --temp，关掉窗口就该结束）都不涉及它。
// linger 是用户级的系统设置、可能被管理员有意关闭，所以：
//   - 开启是尽力而为：失败只提示手动命令，不让安装失败——此时服务本身已经装好并在运行；
//   - 只关闭「由安装器开启」的 linger（数据目录里留一个记号）；本来就开着的、别人开的，卸载时原样保留；
//   - --no-linger 可以明确不要这个行为。

const (
	// lingerDir 是 logind 保存 linger 状态的目录：每个开启了 linger 的用户在这里有一个同名空文件。
	lingerDir = "/var/lib/systemd/linger"
	// lingerMarkerFile 放在数据目录里，内容是开启时的用户名；它存在，才说明这份 linger 是安装器开的。
	lingerMarkerFile = "linger-by-installer"
)

// lingerState 描述一次安装对 linger 做了什么。
type lingerState string

const (
	lingerNA      lingerState = ""        // 不适用：系统级 / 非 Linux
	lingerEnabled lingerState = "enabled" // 由安装器开启（卸载时会一并关闭）
	lingerAlready lingerState = "already" // 本来就是开着的，不是安装器开的
	lingerSkipped lingerState = "skipped" // 使用者用 --no-linger 明确不要
	lingerFailed  lingerState = "failed"  // 想开但没成功
)

// lingerOn 报告某个用户当前是否已开启 linger。
func (s sysOps) lingerOn(name string) bool {
	_, err := os.Stat(s.p(filepath.Join(lingerDir, name)))
	return err == nil
}

// lingerUser 是要开启 linger 的用户名：就是执行安装的这个用户。
func (s sysOps) lingerUser() string {
	return strings.TrimSpace(s.userName)
}

// ensureLinger 为 Linux 用户级安装开启 linger。必须在服务已成功启动之后调用：安装失败并回滚时不能留下 linger 的改动。
// 返回的 detail 只在失败时有内容（命令的输出，供提示里引用）。
func ensureLinger(c common, cfg resolved, s sysOps, scope serviceScope) (st lingerState, name, detail string) {
	if s.goos != "linux" || !scope.user {
		return lingerNA, "", ""
	}
	name = s.lingerUser()
	if c.noLinger {
		return lingerSkipped, name, ""
	}
	if name == "" {
		return lingerFailed, "", "无法确定当前用户名"
	}

	marker := filepath.Join(cfg.dataDir, lingerMarkerFile)
	_, markErr := os.Stat(marker)
	ours := markErr == nil
	switch {
	case s.lingerOn(name) && ours:
		return lingerEnabled, name, "" // 重装 / 升级：保持「由安装器开启」的记号，卸载时仍会关闭
	case s.lingerOn(name):
		return lingerAlready, name, ""
	}

	out, err := s.run("loginctl", "enable-linger", name)
	if err != nil {
		detail = strings.TrimSpace(out)
		if detail == "" {
			detail = err.Error()
		}
		return lingerFailed, name, firstLine(detail)
	}
	// 记号写不下去（数据目录不可写）不影响 linger 本身已经开启；只是卸载时不会替你关，保守起见这样更安全。
	if err := os.MkdirAll(cfg.dataDir, 0o700); err == nil {
		_ = writeFileAtomic(marker, []byte(name+"\n"), 0o600)
	}
	return lingerEnabled, name, ""
}

// takeLingerMarker 在卸载用户级服务时取走记号：返回「安装时由安装器开启了 linger」的那个用户名，并删掉记号文件
// （清数据目录时它不能留下；无论之后关闭是否成功都不再保留，避免留下一个永远清不掉的文件）。没有记号返回空串。
// 这里只取记号，真正关闭 linger 要等所有清理做完，见 disableLinger。
func takeLingerMarker(cfg resolved, s sysOps, scope serviceScope) string {
	if s.goos != "linux" || !scope.user {
		return ""
	}
	marker := filepath.Join(cfg.dataDir, lingerMarkerFile)
	b, err := os.ReadFile(marker)
	if err != nil {
		return ""
	}
	_ = os.Remove(marker)
	if name := strings.TrimSpace(string(b)); name != "" {
		return name
	}
	return s.lingerUser()
}

// disableLinger 关闭安装器当初开启的 linger。必须排在卸载的最后一步：关闭 linger 之后，用户若没有任何登录会话，
// 用户管理器会立刻停止，并连带结束它名下的进程——远程卸载时本进程就是用户管理器下的瞬态单元，更早关闭会把后面的清理截断。
// 返回执行过的命令与一句给使用者的说明（关闭失败时才有）。
func disableLinger(s sysOps, name string) (cmd, note string) {
	cmd = "loginctl disable-linger " + name
	if out, err := s.run("loginctl", "disable-linger", name); err != nil {
		reason := firstLine(strings.TrimSpace(out))
		if reason == "" {
			reason = err.Error()
		}
		note = "没能自动关闭安装时开启的 linger（" + reason + "）；需要时手动执行：" + cmdLine("sudo loginctl disable-linger "+name)
	}
	return cmd, note
}

// lingerManualCmd 是需要使用者手动开启 linger 时给出的命令（先试不带 sudo 的，普通用户给自己开通常不需要管理员）。
func lingerManualCmd(name string) string {
	if name == "" {
		name = "$USER"
	}
	return "loginctl enable-linger " + name
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
