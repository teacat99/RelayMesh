package agent

import (
	"runtime"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Agent 在握手里自报平台标识（GOOS-GOARCH）：服务端据此在分发目录里找对应的二进制并取它的校验值，
// 它必须与 Agent 自己推导下载文件名时用的是同一个值，并且能通过协议层的净化。
func TestHostMetaDeclaresItsPlatform(t *testing.T) {
	m := HostMeta("v1.2.3", termproto.KindPersistent)
	want := runtime.GOOS + "-" + runtime.GOARCH
	if m.Plat != want {
		t.Fatalf("Plat = %q，期望 %q", m.Plat, want)
	}
	if got := m.Sanitized().Plat; got != want {
		t.Fatalf("净化后的 Plat = %q，期望 %q（真实平台的写法必须被协议层接受）", got, want)
	}
}
