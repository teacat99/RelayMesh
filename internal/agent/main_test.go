package agent

import (
	"os"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/relsign/trust"
)

// 这个包的测试讲的是「机制」（同意、时间段、尝试记录、验签流程……）：默认不带任何编进程序的公钥，
// 各用例自己提供要用的钥匙，不依赖发布用的真实公钥——换钥匙、轮换都不会让这些测试变化。
// 发布构建里真正编进去的名单由 internal/relsign/trust 的测试把关。
func TestMain(m *testing.M) {
	restore := trust.SetTextsForTest(nil)
	code := m.Run()
	restore()
	os.Exit(code)
}
