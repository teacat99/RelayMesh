//go:build linux || darwin

package holder

import (
	"os"
	"testing"
)

func TestPeerUIDMatchesCurrentUser(t *testing.T) {
	dataDir := shortDataDir(t)
	opts, _ := fakeRunOptions(dataDir)
	socket, _, _ := startRun(t, opts)
	nc, err := dialRaw(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	uid, ok := peerUID(nc)
	if !ok || uid != os.Geteuid() {
		t.Fatalf("对端用户 = %d (ok=%v)，期望 %d", uid, ok, os.Geteuid())
	}
}
