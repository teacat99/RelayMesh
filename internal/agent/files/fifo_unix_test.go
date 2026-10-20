//go:build !windows

package files

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 对没有写端的 FIFO 做阻塞式 open 会永远卡住；非阻塞打开 + 类型检查必须立刻把它拒绝掉。
func TestFIFOIsRejectedWithoutBlocking(t *testing.T) {
	root, _ := layout(t)
	p := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("无法创建 FIFO: %v", err)
	}
	s := newTestService(t, Config{})
	done := make(chan *Error, 2)
	go func() {
		_, _, e := s.Open([]string{root}, p, 0)
		done <- e
	}()
	go func() {
		_, _, e := s.Hash(context.Background(), []string{root}, p, 0)
		done <- e
	}()
	for i := 0; i < 2; i++ {
		select {
		case e := <-done:
			wantCode(t, e, termproto.FileErrNotFile)
		case <-time.After(5 * time.Second):
			t.Fatal("打开 FIFO 阻塞了")
		}
	}
}
