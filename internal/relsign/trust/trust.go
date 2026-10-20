// Package trust 是编进 Agent 程序的「发布公钥」——静默自动升级的信任根（方案见文档库 design/agent-auto-upgrade.md B.3 与 C 层）。
//
// 单独成包而不放进 relsign，是为了让 Hub 服务端的代码不接触它：Hub 只转交签名、从不验签（见 relsign.ValidSignatureText），
// 验签的信任根只应该存在于 Agent 侧代码与发布工具里——Hub 被攻破时，攻击者既拿不到私钥，也没有一份「信任根」可改写
// （真正生效的那份编在每台主机各自的程序里）。TestOnlyAgentSideCodeImportsTheTrustRoot 把「谁可以 import 它」钉死。
// （主程序 relaymesh 带着 `relaymesh agent` 子命令，所以它的二进制里也有这份名单——那是 Agent 的那一半，不是 Hub 服务端在用。）
//
// 名单里有两把钥匙：
//   - 主钥：发布流程（scripts/deploy-direct.sh → scripts/sign-agent-dist.sh）用它签名；
//   - 备用钥：私钥单独保管，平时不用。主钥丢失时，用它签的下一个版本照样能被已安装的 Agent 接受，再由那个版本把名单换掉。
//
// 两把私钥都放在开发机的 ~/.config/relaymesh/（目录 0700、文件 0600），从不进入仓库、镜像与生产主机；
// 这里只有公钥，公开也无妨。改这份名单等于改信任根：必须同时改 trust_test.go 里钉住的密钥标识，让这件事不会悄悄发生。
package trust

import (
	"crypto/ed25519"
	"strings"
	"sync/atomic"

	"github.com/teacat99/RelayMesh/v2/internal/relsign"
)

// releaseKeyTexts 是编进程序的发布公钥（rmpub1:…），第一把是主钥。
var releaseKeyTexts = []string{
	"rmpub1:e2c755ec7c195a7a65b393227725c37a34f28472820b329efa719e2fbe97f556", // 主钥，标识 90c99847aa98789c
	"rmpub1:522d616453ef16efbc17a066a5eaad9bcb1edb7c98827d9fbe600aed35cd7095", // 备用钥，标识 05f40badb9475e42
}

// override 只能在链接时用 -X github.com/teacat99/RelayMesh/v2/internal/relsign/trust.override=… 设置，运行时没有任何办法改它：
//   - 空（发布构建）：用上面编进去的名单；
//   - none：名单为空（端到端测试用来验证「没有可信公钥时拒绝开启静默模式」）；
//   - rmpub1:…,rmpub1:…：用这几把公钥替换（端到端测试用一次性的测试密钥，不必用真实的发布密钥签测试产物）。
var override string

// overrideNone 是 override 里表示「名单为空」的取值。
const overrideNone = "none"

// MaxKeys 是编进程序的公钥个数上限：名单是信任根，多了说明有人把它当成了普通配置在用。
const MaxKeys = 4

// testTexts 只供测试替换（见 SetTextsForTest）；用原子指针，后台协程在读名单时被测试改写也不会产生数据竞争。
var testTexts atomic.Pointer[[]string]

// Texts 返回当前生效的公钥文本名单（副本）。
func Texts() []string {
	if p := testTexts.Load(); p != nil {
		return append([]string(nil), (*p)...)
	}
	switch o := strings.TrimSpace(override); o {
	case "":
		return append([]string(nil), releaseKeyTexts...)
	case overrideNone:
		return nil
	default:
		var out []string
		for _, part := range strings.Split(o, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	}
}

// Keys 解析当前生效的公钥名单；写错的条目被跳过（trust_test 保证发布的构建里没有写错的条目）。
func Keys() []ed25519.PublicKey {
	texts := Texts()
	out := make([]ed25519.PublicKey, 0, len(texts))
	for _, text := range texts {
		if k, err := relsign.ParsePublicKey(text); err == nil {
			out = append(out, k)
		}
	}
	return out
}

// IDs 返回当前生效名单里每把公钥的标识（与签名里写的密钥标识同一个写法），用于显示与核对。
func IDs() []string {
	keys := Keys()
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = relsign.KeyID(k)
	}
	return out
}

// SetTextsForTest 临时把名单换成 texts（nil 表示空名单），返回恢复函数。只供测试使用：
// 生产代码没有任何地方调用它，链接参数 override 才是构建时调整名单的唯一办法。测试里不要并行地交叉使用它。
func SetTextsForTest(texts []string) (restore func()) {
	prev := testTexts.Load()
	cp := append([]string(nil), texts...)
	testTexts.Store(&cp)
	return func() { testTexts.Store(prev) }
}
