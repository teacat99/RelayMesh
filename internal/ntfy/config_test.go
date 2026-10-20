package ntfy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNormalize_Defaults(t *testing.T) {
	got := Config{Server: "  https://ntfy.example.com/ntfy///  ", Topic: " abc ", Username: " bob ", Password: "p w\r\n", ClickURL: " https://x.test/ "}.Normalize()
	if got.Server != "https://ntfy.example.com/ntfy" {
		t.Errorf("服务地址未去掉空白与末尾斜杠：%q", got.Server)
	}
	if got.Topic != "abc" || got.Username != "bob" || got.ClickURL != "https://x.test/" {
		t.Errorf("字段未去空白：%+v", got)
	}
	if got.Password != "p w" {
		t.Errorf("密码只应去掉换行、保留有意的空格：%q", got.Password)
	}
	if got.Priority != DefaultPriority {
		t.Errorf("优先级默认值不对：%d", got.Priority)
	}

	empty := Config{}.Normalize()
	if empty.Server != DefaultServer {
		t.Errorf("服务地址为空时应回到默认：%q", empty.Server)
	}
	// 模型运行结束的推送：默认关，门槛默认 60 秒；已保存的值不被改动。
	if empty.RunNotify || empty.RunMinSeconds != DefaultRunMinSeconds {
		t.Errorf("运行结束推送应默认关闭、门槛默认 %d 秒：%+v", DefaultRunMinSeconds, empty)
	}
	if kept := (Config{RunMinSeconds: 5}).Normalize(); kept.RunMinSeconds != 5 {
		t.Errorf("已设置的门槛不应被默认值覆盖：%d", kept.RunMinSeconds)
	}
}

// 升级前保存的配置里没有运行结束推送的字段：读出来必须是「关闭 + 默认门槛」，而不是门槛为 0。
func TestNormalize_OldStoredConfigGetsRunDefaults(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"enabled":true,"server":"https://ntfy.sh","topic":"t1","priority":4,"include_summary":false,"delay_seconds":0}`), &c); err != nil {
		t.Fatal(err)
	}
	c = c.Normalize()
	if c.RunNotify || c.RunMinSeconds != DefaultRunMinSeconds {
		t.Errorf("旧配置应得到「关闭 + 默认门槛」：%+v", c)
	}
	if err := c.Validate(true); err != nil {
		t.Errorf("旧配置补默认值后应能通过校验：%v", err)
	}
}

func TestValidate_Table(t *testing.T) {
	ok := Config{Enabled: true, Server: "https://ntfy.sh", Topic: "my-topic_1", Priority: 4}.Normalize()
	cases := []struct {
		name    string
		mutate  func(*Config)
		require bool
		wantErr string // 空表示应通过；否则错误信息应包含它
	}{
		{"合法的基本配置", func(c *Config) {}, true, ""},
		{"带路径前缀的自建地址", func(c *Config) { c.Server = "http://192.168.1.5:8080/ntfy" }, true, ""},
		{"服务地址不是 http", func(c *Config) { c.Server = "ftp://ntfy.sh" }, true, "http://"},
		{"服务地址没有主机", func(c *Config) { c.Server = "https://" }, true, "有效的网址"},
		{"服务地址漏写协议", func(c *Config) { c.Server = "ntfy.example.com" }, true, "http://"},
		{"服务地址漏写协议（带端口）", func(c *Config) { c.Server = "localhost:8080" }, true, "http://"},
		{"服务地址带用户信息", func(c *Config) { c.Server = "https://u:p@ntfy.sh" }, true, "认证项"},
		{"服务地址带查询串", func(c *Config) { c.Server = "https://ntfy.sh/?a=1" }, true, "参数"},
		{"服务地址带空白", func(c *Config) { c.Server = "https://ntfy .sh" }, true, "空白"},
		{"启用时主题必填", func(c *Config) { c.Topic = "" }, true, "主题不能为空"},
		{"未启用时主题可空", func(c *Config) { c.Topic = "" }, false, ""},
		{"主题含空格", func(c *Config) { c.Topic = "a b" }, true, "字母、数字"},
		{"主题含斜杠", func(c *Config) { c.Topic = "a/b" }, true, "字母、数字"},
		{"主题过长", func(c *Config) { c.Topic = strings.Repeat("a", 65) }, true, "64"},
		{"令牌含空白", func(c *Config) { c.Token = "tk_ab cd" }, true, "令牌"},
		{"令牌含换行（请求头注入）", func(c *Config) { c.Token = "tk_x\r\nX-Evil: 1" }, true, "令牌"},
		{"用户名含冒号", func(c *Config) { c.Username, c.Password = "a:b", "p" }, true, "冒号"},
		{"只有用户名没有密码", func(c *Config) { c.Username = "bob" }, true, "同时填写"},
		{"只有密码没有用户名", func(c *Config) { c.Password = "p" }, true, "同时填写"},
		{"用户名密码齐全", func(c *Config) { c.Username, c.Password = "bob", "p@ss w0rd" }, true, ""},
		{"点击地址不是网址", func(c *Config) { c.ClickURL = "javascript:alert(1)" }, true, "点击打开的地址"},
		{"点击地址不是 http", func(c *Config) { c.ClickURL = "ftp://x.test/a" }, true, "http://"},
		{"点击地址可带查询串", func(c *Config) { c.ClickURL = "https://rm.example.com/?x=1#a" }, true, ""},
		{"优先级过高", func(c *Config) { c.Priority = 6 }, true, "优先级"},
		{"优先级为负", func(c *Config) { c.Priority = -1 }, true, "优先级"},
		{"延迟为负", func(c *Config) { c.DelaySeconds = -1 }, true, "延迟"},
		{"延迟超过上限", func(c *Config) { c.DelaySeconds = MaxDelaySeconds + 1 }, true, "延迟"},
		{"延迟取上限", func(c *Config) { c.DelaySeconds = MaxDelaySeconds }, true, ""},
		{"运行结束门槛取下限", func(c *Config) { c.RunNotify, c.RunMinSeconds = true, 1 }, true, ""},
		{"运行结束门槛取上限", func(c *Config) { c.RunNotify, c.RunMinSeconds = true, MaxRunMinSeconds }, true, ""},
		{"运行结束门槛为负", func(c *Config) { c.RunMinSeconds = -1 }, true, "最短运行时长"},
		{"运行结束门槛超过上限", func(c *Config) { c.RunMinSeconds = MaxRunMinSeconds + 1 }, true, "最短运行时长"},
		// 没打开运行结束推送时，门槛也要合法：它会随整张表单保存，存进库里的不能是坏值。
		{"未打开运行结束推送时门槛同样要合法", func(c *Config) { c.RunNotify, c.RunMinSeconds = false, MaxRunMinSeconds+1 }, true, "最短运行时长"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ok
			tc.mutate(&c)
			err := c.Validate(tc.require)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("应通过，却报错：%v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应报错（含 %q），却通过了", tc.wantErr)
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("错误应能用 errors.Is 识别为 ErrInvalidConfig：%v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("错误信息应含 %q，实际：%v", tc.wantErr, err)
			}
		})
	}
}

func TestAuthHeader(t *testing.T) {
	if got := (Config{}).authHeader(); got != "" {
		t.Errorf("没配认证时应为空：%q", got)
	}
	if got := (Config{Token: "tk_abc"}).authHeader(); got != "Bearer tk_abc" {
		t.Errorf("令牌应走 Bearer：%q", got)
	}
	// 用户名:密码 的 Base64
	if got := (Config{Username: "bob", Password: "pw"}).authHeader(); got != "Basic Ym9iOnB3" {
		t.Errorf("用户名密码应走 Basic：%q", got)
	}
	// 两者都配时令牌优先
	if got := (Config{Token: "tk_abc", Username: "bob", Password: "pw"}).authHeader(); got != "Bearer tk_abc" {
		t.Errorf("令牌应优先：%q", got)
	}
}

func TestWithEnvToken(t *testing.T) {
	c := Config{Token: "tk_db"}
	if got := c.WithEnvToken("  tk_env "); got.Token != "tk_env" {
		t.Errorf("环境变量里的令牌应盖过已保存的：%q", got.Token)
	}
	if got := c.WithEnvToken(""); got.Token != "tk_db" {
		t.Errorf("环境变量为空时不应改动：%q", got.Token)
	}
}

// 对外视图绝不能带出凭据：连 JSON 字符串里都不许出现。
func TestPublic_NeverLeaksSecrets(t *testing.T) {
	c := Config{Enabled: true, Topic: "topic1", Token: "tk_SECRETTOKEN", Username: "bob", Password: "SECRETPASS"}.Normalize()
	raw, err := json.Marshal(c.Public(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"tk_SECRETTOKEN", "SECRETPASS"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("对外视图泄露了凭据 %q：%s", secret, raw)
		}
	}
	var view PublicConfig
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if !view.TokenSet || !view.PasswordSet || view.TokenFromEnv {
		t.Errorf("应只告诉界面「已设置」：%+v", view)
	}
	if view.Username != "bob" || view.Topic != "topic1" {
		t.Errorf("非凭据字段应原样给出：%+v", view)
	}

	// 令牌来自环境变量：同样不给值，但标明来源。
	fromEnv := Config{}.Normalize().Public("tk_ENVSECRET")
	raw, _ = json.Marshal(fromEnv)
	if strings.Contains(string(raw), "tk_ENVSECRET") {
		t.Fatalf("泄露了环境变量里的令牌：%s", raw)
	}
	if !fromEnv.TokenSet || !fromEnv.TokenFromEnv {
		t.Errorf("环境变量提供令牌时应标明：%+v", fromEnv)
	}
}

func TestUpdate_ApplyTo(t *testing.T) {
	cur := Config{Enabled: true, Server: "https://old.example.com", Topic: "old", Token: "tk_old", Username: "bob", Password: "oldpw", Priority: 3}.Normalize()

	// 凭据留空 = 保持；非凭据字段整体替换。
	next := Update{Enabled: false, Server: "https://new.example.com", Topic: "newtopic", Username: "bob", Priority: 5, DelaySeconds: 30}.ApplyTo(cur)
	if next.Token != "tk_old" || next.Password != "oldpw" {
		t.Errorf("凭据留空应保持原值：%+v", next)
	}
	if next.Enabled || next.Server != "https://new.example.com" || next.Topic != "newtopic" || next.Priority != 5 || next.DelaySeconds != 30 {
		t.Errorf("非凭据字段应整体替换：%+v", next)
	}

	// 填了新值 = 替换。
	next = Update{Server: "https://x.test", Topic: "t", Username: "bob", Token: "tk_new", Password: "newpw", Priority: 4}.ApplyTo(cur)
	if next.Token != "tk_new" || next.Password != "newpw" {
		t.Errorf("填了新值应替换：%+v", next)
	}

	// clear_ 标记 = 清掉，且优先于同时给出的新值。
	next = Update{Server: "https://x.test", Topic: "t", Username: "bob", Token: "tk_new", ClearToken: true, ClearPassword: true, Priority: 4}.ApplyTo(cur)
	if next.Token != "" || next.Password != "" {
		t.Errorf("clear 标记应清掉凭据：%+v", next)
	}

	// 用户名被清空时，遗留的密码一并清掉，避免莫名的「用户名和密码需要同时填写」。
	next = Update{Server: "https://x.test", Topic: "t", Username: "  ", Priority: 4}.ApplyTo(cur)
	if next.Username != "" || next.Password != "" {
		t.Errorf("清空用户名应连带清掉密码：%+v", next)
	}
	if err := next.Validate(true); err != nil {
		t.Errorf("清空用户名后的配置应能通过校验：%v", err)
	}
}

func TestUpdate_ApplyTo_RunNotifyFields(t *testing.T) {
	cur := Config{Enabled: true, Topic: "t", RunNotify: false}.Normalize()

	next := Update{Enabled: true, Topic: "t", Priority: 4, RunNotify: true, RunMinSeconds: 300}.ApplyTo(cur)
	if !next.RunNotify || next.RunMinSeconds != 300 {
		t.Errorf("运行结束推送的两项应随表单整体替换：%+v", next)
	}

	// 整张表单替换：没带这两项（比如旧版本的页面）就回到「关闭 + 默认门槛」，不会沿用上一次的值。
	cur.RunNotify, cur.RunMinSeconds = true, 300
	next = Update{Enabled: true, Topic: "t", Priority: 4}.ApplyTo(cur)
	if next.RunNotify || next.RunMinSeconds != DefaultRunMinSeconds {
		t.Errorf("没带运行结束推送的字段应回到默认：%+v", next)
	}
}

func TestPublic_CarriesRunNotifyFields(t *testing.T) {
	view := Config{Enabled: true, Topic: "t", RunNotify: true, RunMinSeconds: 90}.Normalize().Public("")
	if !view.RunNotify || view.RunMinSeconds != 90 {
		t.Errorf("对外视图应带出运行结束推送的两项：%+v", view)
	}
	raw, _ := json.Marshal(view)
	for _, key := range []string{`"run_notify":true`, `"run_min_seconds":90`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("对外 JSON 应含 %s：%s", key, raw)
		}
	}
	def := Config{}.Normalize().Public("")
	if def.RunNotify || def.RunMinSeconds != DefaultRunMinSeconds {
		t.Errorf("默认视图应是「关闭 + 默认门槛」：%+v", def)
	}
}
