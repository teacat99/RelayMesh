package buildinfo

import (
	"runtime/debug"
	"testing"
)

// 版本号只允许 vX.Y.Z 与 vX.Y.Z-alpha|beta|rc.N（预览版有自己的 Release）；
// 任何别的写法（含开发版、伪版本、想往脚本或命令里塞内容的）一律留空。
func TestReleaseTag(t *testing.T) {
	cases := map[string]string{
		"v2.0.0":                      "v2.0.0",
		"2.0.0":                       "v2.0.0",
		"  v10.20.30  ":               "v10.20.30",
		"":                            "",
		"dev":                         "",
		"(devel)":                     "",
		"v2.0":                        "",
		"v2.0.0-rc.1":                 "v2.0.0-rc.1",
		"2.0.0-rc.12":                 "v2.0.0-rc.12",
		"v2.0.0-beta.2":               "v2.0.0-beta.2",
		"v2.0.0-alpha.10":             "v2.0.0-alpha.10",
		"v2.0.0-rc":                   "",
		"v2.0.0-rc.":                  "",
		"v2.0.0-rc.1.2":               "",
		"v2.0.0-RC.1":                 "",
		"v2.0.0-pre.1":                "",
		"v2.0.0-rc.1000":              "",
		"v2.0.0-rc.1+build":           "",
		"v2.0.0-rc.1+dirty":           "",
		"v2.0.0+incompatible":         "",
		"v2.0.0-rc.1\nrm -rf /":       "",
		"v2.0.0-rc.1'; id; '":         "",
		"v1.6.3-dev-20261020-15e4726": "",
		"v2.0.0\n":                    "v2.0.0", // 首尾空白会被去掉；输出永远是重新拼出来的 vX.Y.Z
		"v2.0.0\nrm -rf /":            "",
		"v2.0.0'; rm -rf /; echo '":   "",
		"v2.0.0 && id":                "",
		"$(id)":                       "",
		"v12345.0.0":                  "",
		// 伪版本：go run / go install 在没有标签的提交上得到的模块版本，不是 Release。
		"v2.0.1-0.20261021120000-abcdef123456":      "",
		"v2.0.0-rc.3.0.20261021120000-abcdef123456": "",
	}
	for in, want := range cases {
		if got := ReleaseTag(in); got != want {
			t.Errorf("ReleaseTag(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func info(mainVersion string) *debug.BuildInfo {
	return &debug.BuildInfo{Main: debug.Module{Path: "github.com/teacat99/RelayMesh/v2", Version: mainVersion}}
}

// ldflags 注入了真正的版本号就原样使用，构建信息里写什么都不改它（热更用的开发版号、Docker 的无 v 写法都在内）。
func TestResolveKeepsInjectedVersion(t *testing.T) {
	for _, injected := range []string{"v2.0.0-rc.2", "2.0.0", "v1.6.3-dev-20261020-15e4726", "v9.9.9-whatever"} {
		for _, mod := range []string{"", "(devel)", "v2.0.0-rc.1", "v3.0.0"} {
			if got := resolve(injected, info(mod), true); got != injected {
				t.Errorf("resolve(%q, 模块版本 %q) = %q，应原样保留注入值", injected, mod, got)
			}
		}
		if got := resolve(injected, nil, false); got != injected {
			t.Errorf("没有构建信息时 resolve(%q) = %q，应原样保留注入值", injected, got)
		}
	}
}

// 没有 ldflags 时，模块版本是正式 / 预发布标签才采用（go run …@v2.0.0-rc.2 / go install …@v2.0.0 的场景）。
func TestResolveFallsBackToModuleVersion(t *testing.T) {
	for _, injected := range []string{"", "dev", "  dev  "} {
		cases := map[string]string{
			"v2.0.0-rc.2":                          "v2.0.0-rc.2",
			"v2.0.0":                               "v2.0.0",
			"v2.1.0-beta.1":                        "v2.1.0-beta.1",
			"2.0.0":                                "v2.0.0", // 模块版本本来都带 v；万一没有，也规范成标签写法
			"":                                     "dev",
			"(devel)":                              "dev",
			"v2.0.0-rc.2+dirty":                    "dev", // 工作区有改动的构建不是 Release
			"v2.0.1-0.20261021120000-abcdef123456": "dev",
			"v2.0.0-rc.3.0.20261021120000-abcdef123456": "dev",
			"v2.0.0+incompatible":                       "dev",
			"v2.0.0-rc.2\nrm -rf /":                     "dev",
		}
		for mod, want := range cases {
			if got := resolve(injected, info(mod), true); got != want {
				t.Errorf("resolve(%q, 模块版本 %q) = %q，想要 %q", injected, mod, got, want)
			}
		}
		// 读不到构建信息（理论上只有被 strip 掉模块信息的构建）：保持 dev。
		if got := resolve(injected, nil, false); got != DevVersion {
			t.Errorf("没有构建信息时 resolve(%q) = %q，想要 dev", injected, got)
		}
		if got := resolve(injected, nil, true); got != DevVersion {
			t.Errorf("构建信息为空指针时 resolve(%q) = %q，想要 dev", injected, got)
		}
	}
}

// go test 的测试二进制不是 Release 构建：全局入口应得到 dev（而不是误取到什么模块版本）。
func TestResolveInTestBinaryIsDev(t *testing.T) {
	if got := Resolve("dev"); got != DevVersion {
		t.Fatalf("测试二进制里 Resolve(\"dev\") = %q，想要 dev", got)
	}
	if got := Resolve("v2.0.0-rc.2"); got != "v2.0.0-rc.2" {
		t.Fatalf("注入的版本号应原样返回，得到 %q", got)
	}
}
