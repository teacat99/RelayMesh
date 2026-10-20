package cli

import (
	"flag"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
)

// 环境变量名：与命令行选项一一对应，方便容器 / 服务管理器里用环境变量配置。
const (
	envServer    = "RELAYMESH_AGENT_SERVER"
	envPin       = "RELAYMESH_AGENT_PIN"
	envInsecure  = "RELAYMESH_AGENT_INSECURE"
	envTemp      = "RELAYMESH_AGENT_TEMP"
	envDataDir   = "RELAYMESH_AGENT_DATA_DIR"
	envConfigDir = "RELAYMESH_AGENT_CONFIG_DIR"
	envLogFile   = "RELAYMESH_AGENT_LOG_FILE"

	envHolder       = "RELAYMESH_AGENT_HOLDER"
	envHolderSocket = "RELAYMESH_AGENT_HOLDER_SOCKET"
)

// stringList 是可重复的字符串选项（--pin a --pin b）。
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// common 是 run 与 install 共用的选项。
type common struct {
	server      string
	pins        stringList
	insecure    bool
	temp        bool
	persist     bool
	dataDir     string
	configDir   string
	noTOFU      bool
	maxSessions int
	user        bool
	highest     bool // 仅 Windows，与 user 合用：以当前用户身份、最高权限运行（需要管理员来创建任务）
	noLinger    bool // 仅 Linux 用户级安装：不要自动开启 linger
	logFile     string
	noElevate   bool
	relaunched  bool

	holderMode   string
	holderSocket string

	recordFlags
	execFlags
	fileFlags
	sshFlags
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// bind 把选项注册到 fs，默认值取自环境变量（命令行显式给出的值优先）。
func (c *common) bind(fs *flag.FlagSet, env Env) {
	fs.StringVar(&c.server, "server", env.Getenv(envServer), "RelayMesh 服务端地址")
	for _, p := range strings.Split(env.Getenv(envPin), ",") {
		if p = strings.TrimSpace(p); p != "" {
			c.pins = append(c.pins, p)
		}
	}
	fs.Var(&c.pins, "pin", "固定服务端证书指纹 sha256:<hex>（可重复）")
	fs.BoolVar(&c.insecure, "insecure", truthy(env.Getenv(envInsecure)), "允许明文 ws://")
	fs.BoolVar(&c.temp, "temp", truthy(env.Getenv(envTemp)), "使用临时身份")
	fs.BoolVar(&c.persist, "persist", false, "显式使用持久身份（run 的默认行为；与 --temp 互斥，并压过环境变量 "+envTemp+"）")
	fs.StringVar(&c.dataDir, "data-dir", env.Getenv(envDataDir), "身份文件目录")
	fs.StringVar(&c.configDir, "config-dir", env.Getenv(envConfigDir), "配置目录（已信任服务器记录）")
	fs.BoolVar(&c.noTOFU, "no-tofu", false, "不在首次连接时询问是否信任服务端证书")
	fs.IntVar(&c.maxSessions, "max-sessions", 0, "本机会话数上限（0 表示默认值；服务端策略只能更严格）")
	fs.BoolVar(&c.user, "user", false, "仅为当前用户安装 / 卸载服务")
	fs.BoolVar(&c.highest, "highest", false, "Windows：与 --user 合用，以当前用户身份、最高权限运行（登录时自动带管理员令牌启动，无 UAC 弹窗；创建任务需要管理员）")
	fs.BoolVar(&c.noLinger, "no-linger", false, "Linux：用户级安装时不要自动执行 loginctl enable-linger（默认会开启，让服务在退出登录后继续运行）")
	fs.StringVar(&c.logFile, "log-file", env.Getenv(envLogFile), "把日志写入该文件（自动轮转）；用于没有控制台的后台运行（如 Windows 计划任务）")
	fs.BoolVar(&c.noElevate, "no-elevate", truthy(env.Getenv(envNoElevate)), "Windows：临时运行时不请求管理员权限（UAC）")
	fs.BoolVar(&c.relaunched, "relaunched", false, "内部使用：表示这是提权后重新启动的子进程")
	fs.StringVar(&c.holderMode, "holder", env.Getenv(envHolder), "终端会话由谁持有：auto（默认；有会话守护进程就交给它，否则内嵌）/ external（只用守护进程）/ embedded（与 Agent 同进程，不具持久性）")
	fs.StringVar(&c.holderSocket, "holder-socket", env.Getenv(envHolderSocket), "会话守护进程的本地套接字路径（默认在数据目录下）")
	c.recordFlags.bind(fs, env)
	c.execFlags.bind(fs, env)
	c.fileFlags.bind(fs, env)
	c.sshFlags.bind(fs, env)
}

// checkHighest 校验 --highest 的适用范围：它只描述「当前用户的计划任务用什么令牌运行」，
// 所以必须与 --user 合用（系统级任务本来就以 SYSTEM 最高权限运行），且只有 Windows 才有这个概念。
func (c *common) checkHighest(goos string) error {
	if !c.highest {
		return nil
	}
	if goos != "windows" {
		return fmt.Errorf("--highest 仅适用于 Windows（Linux / macOS 请用 sudo 安装系统级服务，或 --user 安装用户级服务）")
	}
	if !c.user {
		return fmt.Errorf("--highest 需要与 --user 合用：它表示「当前用户身份 + 最高权限」；系统级安装（不加 --user）本来就以 SYSTEM 最高权限运行")
	}
	return nil
}

// settle 处理选项之间的关系，必须在 Parse 之后调用。
// run 默认就是持久身份，--persist 只是把这一点写明（沿用方案里的命令形态）；它压过环境变量里的 RELAYMESH_AGENT_TEMP，
// 但与命令行上显式给出的 --temp 互斥。
func (c *common) settle(fs *flag.FlagSet) error {
	if !c.persist {
		return nil
	}
	explicitTemp := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "temp" {
			explicitTemp = true
		}
	})
	if explicitTemp && c.temp {
		return fmt.Errorf("--persist 与 --temp 互斥")
	}
	c.temp = false
	return nil
}

// newFlagSet 创建一个把错误输出到 stderr 的 FlagSet。
func newFlagSet(name string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {}
	return fs
}

// parse 解析参数；flag 包已经把错误打印到 stderr。
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("多余的参数: %s", strings.Join(fs.Args(), " "))
	}
	return nil
}

// resolved 是校验之后的连接配置。
type resolved struct {
	target  agent.ServerTarget
	trust   agent.TrustOptions
	dataDir string
	cfgDir  string
	limits  agent.Limits

	holderMode   agent.HolderMode
	holderSocket string

	record record.Options
	exec   holder.ExecOptions
	ssh    *holder.SSHOptions // 非 nil 表示 SSH 网关处于开启状态（默认开启；临时身份与 --no-ssh-gateway 为 nil）
	files  agent.FileOptions
}

// resolve 校验选项并补全默认值。所有能在启动时发现的配置错误都在这里失败，而不是运行时无限重试。
func (c *common) resolve(env Env, interactive bool) (resolved, error) {
	var r resolved
	if strings.TrimSpace(c.server) == "" {
		return r, fmt.Errorf("缺少服务端地址：请用 --server 指定（例如 --server https://relay.example.com），或设置环境变量 %s", envServer)
	}
	target, err := agent.ParseServer(c.server)
	if err != nil {
		return r, err
	}
	r.target = target
	r.trust = agent.TrustOptions{Pins: append([]string(nil), c.pins...), Insecure: c.insecure}
	if err := r.trust.Validate(target); err != nil {
		return r, err
	}

	r.dataDir = c.dataDir
	if r.dataDir == "" {
		r.dataDir = agent.DefaultDataDir()
	}
	r.cfgDir = c.configDir
	if r.cfgDir == "" {
		r.cfgDir = agent.DefaultConfigDir()
	}

	// TOFU：仅在交互式前台、没有指定指纹、且不是明文时启用。
	if interactive && !c.noTOFU && len(r.trust.Pins) == 0 && target.Secure {
		r.trust.KnownServersPath = filepath.Join(r.cfgDir, "known_servers")
		r.trust.Prompt = func(host, fp string) bool {
			env.printf("\n服务端 %s 出示的证书不在系统信任链内（常见于自签名证书）。\n", host)
			env.printf("证书指纹: %s\n", fp)
			env.printf("请与 RelayMesh 服务端所在环境核对该指纹；确认无误再信任。\n")
			return confirm(env, "是否信任该服务端并记住它？")
		}
	}

	r.limits = agent.DefaultLimits()
	if c.maxSessions < 0 {
		return r, fmt.Errorf("--max-sessions 不能为负数")
	}
	if c.maxSessions > 0 {
		r.limits.MaxSessions = c.maxSessions
	}
	if err := c.resolveHolder(&r); err != nil {
		return r, err
	}
	if err := c.recordFlags.validate(); err != nil {
		return r, err
	}
	// 临时身份不留痕：不录制、也不提供历史读取（Dir 为空）。
	if !c.temp {
		r.record = c.recordFlags.options(r.dataDir)
	}
	// 临时身份同样不支持模型执行（没有可信的落盘位置，机器码也随进程消失）；一票否决照常生效。
	r.exec = c.execFlags.options(r.dataDir, !c.temp)
	r.files = c.fileFlags.options()
	if err := c.sshFlags.validate(); err != nil {
		return r, err
	}
	// 临时身份的机器码随进程消失，不能充当网关：网关上的会话与记录需要一个长期稳定的身份。
	// 网关默认开启，所以临时身份只是静默不提供；命令行明确要求「--temp --ssh-gateway」（或给了 --ssh-allow）才是自相矛盾。
	if c.temp {
		if c.sshFlags.forced() {
			return r, fmt.Errorf("--ssh-gateway 不能与 --temp 同用：网关需要长期稳定的身份")
		}
		if len(c.sshFlags.allow) > 0 {
			return r, fmt.Errorf("--ssh-allow 不能与 --temp 同用：临时身份不提供 SSH 网关")
		}
		return r, nil
	}
	r.ssh = c.sshFlags.options(r.dataDir, env)
	return r, nil
}

// resolveHolder 确定会话持有方式与守护进程的套接字路径。
// 临时身份只能内嵌：它的机器码随进程消失，留在守护进程里的会话以后没有任何身份能再接管。
func (c *common) resolveHolder(r *resolved) error {
	mode, err := agent.ParseHolderMode(c.holderMode)
	if err != nil {
		return err
	}
	if c.temp {
		if mode == agent.HolderExternal {
			return fmt.Errorf("--temp 是临时身份，不能与 --holder external 同用（会话会成为无人能接管的孤儿）")
		}
		mode = agent.HolderEmbedded
	}
	r.holderMode = mode
	r.holderSocket = c.holderSocket
	if r.holderSocket == "" {
		r.holderSocket = holder.SocketPath(r.dataDir)
	}
	return nil
}

// wantsHolder 报告安装时是否应部署独立的会话守护进程：默认（auto）与 external 都要，只有 embedded 与临时身份不要。
// 取值非法时按默认处理——非法值在 resolve 阶段已经报过错，走到安装不会出现。
func (c *common) wantsHolder() bool {
	if c.temp {
		return false
	}
	mode, err := agent.ParseHolderMode(c.holderMode)
	if err != nil {
		return true
	}
	return mode != agent.HolderEmbedded
}

// serviceArgs 把选项还原成服务单元里 `run` 之后的参数。
// 只写入用户显式给出的值——默认目录由运行时按「有效用户」决定，避免把安装者的路径固化进单元。
func (c *common) serviceArgs() []string {
	args := []string{"run", "--server", c.server}
	for _, p := range c.pins {
		args = append(args, "--pin", p)
	}
	if c.insecure {
		args = append(args, "--insecure")
	}
	if c.dataDir != "" {
		args = append(args, "--data-dir", c.dataDir)
	}
	if c.configDir != "" {
		args = append(args, "--config-dir", c.configDir)
	}
	if c.maxSessions > 0 {
		args = append(args, "--max-sessions", strconv.Itoa(c.maxSessions))
	}
	if c.logFile != "" {
		args = append(args, "--log-file", c.logFile)
	}
	args = append(args, c.recordFlags.args()...)
	args = append(args, c.execFlags.args()...)
	args = append(args, c.sshFlags.args()...)
	return append(args, c.fileFlags.args()...)
}

// agentServiceArgs 是 Agent 服务单元里 `run` 之后的参数。
// 守护进程模式下 Agent 只使用守护进程（external）：守护进程暂时不可用时在界面上如实显示「不可用」，
// 而不是悄悄退化成会随 Agent 结束的内嵌会话；内嵌模式则明确写出 embedded，不依赖运行时探测。
func (c *common) agentServiceArgs(withHolder bool) []string {
	args := c.serviceArgs()
	if !withHolder {
		return append(args, "--holder", "embedded")
	}
	args = append(args, "--holder", "external")
	if c.holderSocket != "" {
		args = append(args, "--holder-socket", c.holderSocket)
	}
	return args
}

// holderServiceArgs 是会话守护进程服务单元里的参数。
func (c *common) holderServiceArgs() []string {
	args := []string{"holder", "run"}
	if c.dataDir != "" {
		args = append(args, "--data-dir", c.dataDir)
	}
	if c.holderSocket != "" {
		args = append(args, "--holder-socket", c.holderSocket)
	}
	if c.maxSessions > 0 {
		args = append(args, "--max-sessions", strconv.Itoa(c.maxSessions))
	}
	if c.logFile != "" {
		args = append(args, "--log-file", siblingLogFile(c.logFile, "holder"))
	}
	args = append(args, c.recordFlags.args()...)
	args = append(args, c.execFlags.args()...)
	return append(args, c.sshFlags.args()...)
}

// siblingLogFile 由 Agent 的日志文件名推出同目录下另一个进程的日志文件名（agent.log → agent-holder.log），
// 避免两个进程写同一个文件、轮转时互相干扰。
func siblingLogFile(path, suffix string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "-" + suffix + ext
}
