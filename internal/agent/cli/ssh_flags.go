package cli

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

const (
	// envSSHGateway 只能用来关闭网关（off / 0 / false / no）；开启本来就是默认，写 1 / on 也不改变任何事。
	envSSHGateway = "RELAYMESH_AGENT_SSH_GATEWAY"
	envSSHAllow   = "RELAYMESH_AGENT_SSH_ALLOW"
	// envSSHCredKey 是网关凭据库的外置密封密钥（至少 16 个字符）。不设置则使用数据目录里自动生成的 key 文件；
	// 它只能来自环境变量：命令行参数会出现在进程列表里，服务单元也不会替你写入它。
	envSSHCredKey = "RELAYMESH_AGENT_SSH_CRED_KEY"
)

// falsy 报告环境变量的取值是否明确表示「关闭」。
func falsy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// gatewaySwitch 是 --ssh-gateway 的取值。
//
// 网关是 Agent 的标配能力，默认开启：不写这个选项、写 --ssh-gateway 都是开，--ssh-gateway=false（或 --no-ssh-gateway）关闭。
// explicit 记录命令行是否明确写过它：临时身份下，默认开启的网关会静默不提供，而明确要求「--temp --ssh-gateway」仍然报错。
//
// 内部存的是「是否被关闭」：这样零值就是默认（开启），手工构造的 sshFlags 也不会意外变成关闭。
type gatewaySwitch struct {
	off      bool
	explicit bool
}

func (g *gatewaySwitch) String() string {
	if g == nil {
		return "true"
	}
	return strconv.FormatBool(!g.off)
}

func (g *gatewaySwitch) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("需要 true 或 false")
	}
	g.off, g.explicit = !v, true
	return nil
}

// IsBoolFlag 让 --ssh-gateway 可以不带值（等价于 =true），与普通布尔选项一致。
func (g *gatewaySwitch) IsBoolFlag() bool { return true }

// sshFlags 是 SSH 网关（人工 SSH 终端）的本机选项。
//
// 网关让这台主机替服务端去连接「没有安装 Agent 的 SSH 主机」（例如内网里的服务器）：连接由本机的会话守护进程持有，
// 所以浏览器刷新、服务端重启都不会中断，终端内容的记录也只存在本机。
// 它和「主机服务」一样是 Agent 的标配能力、默认开启，不分版本；想让某台机器不承担中转，用 --no-ssh-gateway（或环境变量
// RELAYMESH_AGENT_SSH_GATEWAY=off）关闭，也可以用 --ssh-allow 把目标限制在指定的网段 / 主机范围内。
type sshFlags struct {
	gw    gatewaySwitch
	off   bool // --no-ssh-gateway
	allow stringList
}

func (sf *sshFlags) bind(fs *flag.FlagSet, env Env) {
	sf.gw.off = falsy(env.Getenv(envSSHGateway))
	fs.Var(&sf.gw, "ssh-gateway",
		"SSH 网关（默认开启）：让服务端借这台主机去连接没有安装 Agent 的 SSH 主机；写 --ssh-gateway=false 或 --no-ssh-gateway 关闭")
	fs.BoolVar(&sf.off, "no-ssh-gateway", false, "关闭 SSH 网关（这台主机只提供自己的终端 / 执行能力，不替服务端连接别的主机）")
	for _, e := range strings.Split(env.Getenv(envSSHAllow), ",") {
		if e = strings.TrimSpace(e); e != "" {
			sf.allow = append(sf.allow, e)
		}
	}
	fs.Var(&sf.allow, "ssh-allow", "SSH 网关只允许连接这些目标：CIDR（10.0.0.0/8）、IP 或主机名（可含 * 通配）；可重复，也可用环境变量 "+envSSHAllow+"（逗号分隔）")
}

// enabled 报告网关是否开启（临时身份的特殊处理由调用方负责）。
func (sf *sshFlags) enabled() bool { return !sf.gw.off && !sf.off }

// forced 报告命令行是否明确要求开启网关（而不是沿用默认）。
func (sf *sshFlags) forced() bool { return sf.gw.explicit && sf.enabled() }

// validate 检查组合是否合理：允许名单只在网关开启时才有意义，条目必须是有效的网段 / 主机名模式。
func (sf *sshFlags) validate() error {
	if sf.gw.explicit && !sf.gw.off && sf.off {
		return fmt.Errorf("--ssh-gateway 与 --no-ssh-gateway 互相矛盾")
	}
	if !sf.enabled() {
		if len(sf.allow) > 0 {
			return fmt.Errorf("--ssh-allow 只在 SSH 网关开启时才有意义（现在网关已被关闭）")
		}
		return nil
	}
	return (&holder.SSHOptions{Allow: sf.allow}).Validate()
}

// options 折算成守护进程的网关选项；已关闭时返回 nil（nil 即「没有网关能力」）。
// 目标的登录凭据只保存在这台网关自己的数据目录里（<数据目录>/ssh-gateway，密封存放），服务端不保管。
func (sf *sshFlags) options(dataDir string, env Env) *holder.SSHOptions {
	if !sf.enabled() {
		return nil
	}
	return &holder.SSHOptions{
		Allow:   append([]string(nil), sf.allow...),
		CredDir: holder.SSHCredDir(dataDir),
		CredKey: strings.TrimSpace(env.Getenv(envSSHCredKey)),
		Harden:  agent.HardenDir,
	}
}

// args 还原成服务单元里的参数：只写与默认值不同的部分——关闭时写 --no-ssh-gateway，开启时只写 --ssh-allow。
// 因此默认安装的服务单元里没有任何 ssh 参数，旧版本写入过的 --ssh-gateway 也依然被接受。
func (sf *sshFlags) args() []string {
	if !sf.enabled() {
		return []string{"--no-ssh-gateway"}
	}
	var out []string
	for _, a := range sf.allow {
		out = append(out, "--ssh-allow", a)
	}
	return out
}
