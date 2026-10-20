package cli

import (
	"flag"
	"path/filepath"
	"strings"
	"testing"
)

func bindSSH(t *testing.T, env Env, args ...string) *common {
	t.Helper()
	c := &common{server: "https://relay.example.com"}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return c
}

// 网关与主机服务一样默认开启，而且默认安装的服务单元里没有任何多余的 ssh 参数。
func TestSSHGatewayIsOnByDefault(t *testing.T) {
	env := testEnv(t)
	c := bindSSH(t, env)
	dataDir := t.TempDir()
	o := c.sshFlags.options(dataDir, env)
	if o == nil {
		t.Fatal("默认应开启 SSH 网关")
	}
	if want := filepath.Join(dataDir, "ssh-gateway"); o.CredDir != want || o.CredKey != "" || o.Harden == nil || len(o.Allow) != 0 {
		t.Fatalf("默认选项不对: %+v", o)
	}
	if err := c.sshFlags.validate(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{c.serviceArgs(), c.agentServiceArgs(true), c.holderServiceArgs()} {
		if strings.Contains(strings.Join(args, " "), "ssh") {
			t.Fatalf("默认的服务单元参数里不应出现 ssh: %v", args)
		}
	}
}

// 三种关闭方式：--no-ssh-gateway、--ssh-gateway=false、环境变量 off；关闭后选项为 nil，并把关闭写回两个服务单元。
func TestSSHGatewayCanBeDisabled(t *testing.T) {
	envOff := testEnv(t)
	envOff.Getenv = func(k string) string {
		if k == envSSHGateway {
			return " OFF "
		}
		return ""
	}
	cases := map[string]*common{
		"--no-ssh-gateway":    bindSSH(t, testEnv(t), "--no-ssh-gateway"),
		"--ssh-gateway=false": bindSSH(t, testEnv(t), "--ssh-gateway=false"),
		"环境变量 off":            bindSSH(t, envOff),
	}
	for name, c := range cases {
		if o := c.sshFlags.options(t.TempDir(), testEnv(t)); o != nil {
			t.Errorf("%s：应关闭网关", name)
		}
		if err := c.sshFlags.validate(); err != nil {
			t.Errorf("%s：%v", name, err)
		}
		for _, args := range [][]string{c.agentServiceArgs(true), c.holderServiceArgs()} {
			if !strings.Contains(strings.Join(args, " "), "--no-ssh-gateway") {
				t.Errorf("%s：服务单元里应写入 --no-ssh-gateway: %v", name, args)
			}
		}
	}
	// 环境变量写 1 / on 只是重复默认值，不算「明确要求」。
	envOn := testEnv(t)
	envOn.Getenv = func(k string) string {
		if k == envSSHGateway {
			return "1"
		}
		return ""
	}
	c := bindSSH(t, envOn)
	if c.sshFlags.options(t.TempDir(), envOn) == nil || c.sshFlags.forced() {
		t.Error("环境变量 1 应保持开启，且不算命令行明确要求")
	}
}

// 旧版本写进服务单元的 --ssh-gateway 仍被接受；允许名单还原进 Agent 与守护进程两个服务单元（守护进程才是真正去连接的一方），
// 但不再重复写 --ssh-gateway（那是默认值）。
func TestSSHGatewayFlagAndServiceArgs(t *testing.T) {
	env := testEnv(t)
	c := bindSSH(t, env, "--ssh-gateway", "--ssh-allow", "10.0.0.0/8", "--ssh-allow", "*.corp.example")
	dataDir := t.TempDir()
	o := c.sshFlags.options(dataDir, env)
	if o == nil || len(o.Allow) != 2 || o.Allow[0] != "10.0.0.0/8" || o.Allow[1] != "*.corp.example" {
		t.Fatalf("选项不对: %+v", o)
	}
	if !c.sshFlags.forced() {
		t.Error("命令行写了 --ssh-gateway，应记为明确要求")
	}
	if err := c.sshFlags.validate(); err != nil {
		t.Fatal(err)
	}
	want := "--ssh-allow 10.0.0.0/8 --ssh-allow *.corp.example"
	for name, args := range map[string][]string{"Agent": c.agentServiceArgs(true), "守护进程": c.holderServiceArgs()} {
		got := strings.Join(args, " ")
		if !strings.Contains(got, want) {
			t.Errorf("%s服务单元缺少允许名单:\n%s", name, got)
		}
		if strings.Contains(got, "ssh-gateway") {
			t.Errorf("%s服务单元不应再写入默认值 --ssh-gateway:\n%s", name, got)
		}
	}
	// 选项必须是拷贝：之后改动解析结果不应影响已交出去的选项。
	c.sshFlags.allow[0] = "changed"
	if o.Allow[0] != "10.0.0.0/8" {
		t.Error("options() 应返回独立的拷贝")
	}
}

func TestSSHGatewayEnvironment(t *testing.T) {
	env := testEnv(t)
	env.Getenv = func(k string) string {
		switch k {
		case envSSHAllow:
			return " 192.168.0.0/16 , jump.lab ,"
		case envSSHCredKey:
			return "  external-cred-key-0123456789  "
		}
		return ""
	}
	c := bindSSH(t, env, "--ssh-allow", "10.1.0.0/16")
	o := c.sshFlags.options(t.TempDir(), env)
	if o == nil {
		t.Fatal("默认应开启网关")
	}
	if o.CredKey != "external-cred-key-0123456789" {
		t.Fatalf("外置密钥应取自环境变量（并去掉首尾空白）: %q", o.CredKey)
	}
	got := strings.Join(o.Allow, "|")
	if got != "192.168.0.0/16|jump.lab|10.1.0.0/16" {
		t.Fatalf("环境变量与命令行的名单应合并（环境在前）: %s", got)
	}
}

func TestSSHFlagCombinationsAreValidated(t *testing.T) {
	// 关闭了网关却给了允许名单：没有意义，启动前报错。
	c := bindSSH(t, testEnv(t), "--no-ssh-gateway", "--ssh-allow", "10.0.0.0/8")
	if err := c.sshFlags.validate(); err == nil || !strings.Contains(err.Error(), "--ssh-allow") {
		t.Fatalf("关闭网关还给允许名单应报错: %v", err)
	}
	// 同时要求开启与关闭：自相矛盾。
	c = bindSSH(t, testEnv(t), "--ssh-gateway", "--no-ssh-gateway")
	if err := c.sshFlags.validate(); err == nil || !strings.Contains(err.Error(), "矛盾") {
		t.Fatalf("开关互相矛盾应报错: %v", err)
	}
	// 默认开启时只给 --ssh-allow 是正常用法。
	c = bindSSH(t, testEnv(t), "--ssh-allow", "10.0.0.0/8")
	if err := c.sshFlags.validate(); err != nil {
		t.Fatalf("默认开启时只给允许名单应通过: %v", err)
	}
	c = bindSSH(t, testEnv(t), "--ssh-allow", "10.0.0.0/99")
	if err := c.sshFlags.validate(); err == nil || !strings.Contains(err.Error(), "CIDR") {
		t.Fatalf("非法 CIDR 应在启动前报错: %v", err)
	}
	c = bindSSH(t, testEnv(t), "--ssh-allow", "bad host")
	if err := c.sshFlags.validate(); err == nil {
		t.Fatal("非法主机名模式应在启动前报错")
	}
	// 非法的开关取值由 flag 包拒绝。
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	(&common{}).bind(fs, testEnv(t))
	if err := fs.Parse([]string{"--ssh-gateway=maybe"}); err == nil {
		t.Fatal("--ssh-gateway=maybe 应被拒绝")
	}
}

// 临时身份没有长期稳定的机器码，不能当网关：默认开启的网关静默不提供；明确要求才报错。
func TestSSHGatewayWithTempIdentity(t *testing.T) {
	env := testEnv(t)
	resolveWith := func(args ...string) (resolved, error) {
		c := bindSSH(t, env, append([]string{"--temp", "--insecure", "--server", "ws://127.0.0.1:1"}, args...)...)
		c.dataDir = t.TempDir()
		return c.resolve(env, false)
	}
	r, err := resolveWith()
	if err != nil {
		t.Fatalf("--temp 不加任何 ssh 参数应正常启动: %v", err)
	}
	if r.ssh != nil {
		t.Fatal("临时身份不应提供 SSH 网关")
	}
	if _, err := resolveWith("--ssh-gateway"); err == nil || !strings.Contains(err.Error(), "--temp") {
		t.Fatalf("明确要求 --temp --ssh-gateway 应报错: %v", err)
	}
	if _, err := resolveWith("--ssh-allow", "10.0.0.0/8"); err == nil || !strings.Contains(err.Error(), "--temp") {
		t.Fatalf("临时身份给了 --ssh-allow 应报错: %v", err)
	}
	if _, err := resolveWith("--no-ssh-gateway"); err != nil {
		t.Fatalf("--temp --no-ssh-gateway 是自洽的: %v", err)
	}
	// 持久身份下默认开启。
	c := bindSSH(t, env, "--insecure", "--server", "ws://127.0.0.1:1")
	c.dataDir = t.TempDir()
	if r, err := c.resolve(env, false); err != nil || r.ssh == nil {
		t.Fatalf("持久身份应默认开启网关: ssh=%v err=%v", r.ssh, err)
	}
}
