package cli

import (
	"os"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/relsign/trust"
)

// 这个包里关于自动升级的测试讲的是「命令的行为」：默认不带任何编进程序的公钥，
// 需要「编进程序的公钥」的用例自己用 trust.SetTextsForTest 提供，不依赖发布用的真实公钥。
// 发布构建里真正编进去的名单由 internal/relsign/trust 的测试把关。
func TestMain(m *testing.M) {
	restore := trust.SetTextsForTest(nil)
	code := m.Run()
	restore()
	os.Exit(code)
}
