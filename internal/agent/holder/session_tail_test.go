package holder

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// emitNumbered 让终端输出 n 行带序号的输出（每行 80 字节左右，CRLF 结尾），并等它们全部进入回放缓冲。
func emitNumbered(t *testing.T, s *Session, term *ptyhost.FakeTerminal, from, n int) {
	t.Helper()
	want := s.info().End
	var b bytes.Buffer
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&b, "%07d  %s\r\n", i, "The quick brown fox jumps over the lazy dog, again and again and again.")
	}
	want += uint64(b.Len())
	term.Emit(b.Bytes())
	eventually(t, "输出进入缓冲", func() bool { return s.info().End == want })
}

func attachTail(t *testing.T, s *Session, sink frameSink, ch uint32, since uint64, tail uint16) attachResult {
	t.Helper()
	res, err := s.attach(sink, ch, since, 0, 0, tail)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	res.ch.start()
	return res
}

// recorded 是会话记录正常录制的场景（裁剪回放的前提）。
func recorded(t *testing.T) *recEnv {
	t.Helper()
	return newRecEnv(t, record.Options{}, Limits{})
}

func TestAttachTailReplaysOnlyLastLines(t *testing.T) {
	e := recorded(t)
	s, term := e.open(t)
	emitNumbered(t, s, term, 0, 5000) // 约 400 KiB：过了 TailMinBytes，也没有绕过 1 MiB 的缓冲
	end := s.info().End
	if end < termproto.TailMinBytes {
		t.Fatalf("测试前提：缓冲量应超过门槛，得到 %d", end)
	}

	sink := &recSink{}
	res := attachTail(t, s, sink, 1, 0, 300)
	r := res.reply
	if r.Truncated {
		t.Fatalf("有意跳过的部分不算被覆盖: %+v", r)
	}
	if r.Base != 0 || r.From == 0 || r.From >= r.End {
		t.Fatalf("应从中间的行首开始回放: %+v", r)
	}
	eventually(t, "回放完成", func() bool { return sink.outLen(1) == int(r.End-r.From) })
	got := sink.payload(t, 1, r.From)
	if got := bytes.Count(got, []byte("\n")); got != 300 {
		t.Fatalf("应回放 300 行，得到 %d", got)
	}
	if !bytes.HasPrefix(got, []byte("0004700  ")) {
		t.Fatalf("回放应从第 4700 行开始: %q", got[:20])
	}
	if !bytes.HasSuffix(got, []byte("again.\r\n")) {
		t.Fatalf("回放应以最后一行结束: %q", got[len(got)-16:])
	}
	if int(r.End-r.From) >= int(end)/2 {
		t.Fatalf("裁剪后的回放应远小于整体: %d / %d", r.End-r.From, end)
	}
}

func TestAttachTailKeepsFullReplayWhenBufferSmall(t *testing.T) {
	e := recorded(t)
	s, term := e.open(t)
	emitNumbered(t, s, term, 0, 400) // 约 32 KiB，不到门槛
	res := attachTail(t, s, &recSink{}, 1, 0, 50)
	if res.reply.From != 0 || res.reply.Truncated {
		t.Fatalf("缓冲还小时应整体回放: %+v", res.reply)
	}
}

func TestAttachTailZeroOrAbsentIsFullReplay(t *testing.T) {
	e := recorded(t)
	s, term := e.open(t)
	emitNumbered(t, s, term, 0, 5000)
	res := attachTail(t, s, &recSink{}, 1, 0, 0)
	if res.reply.From != 0 || res.reply.Truncated {
		t.Fatalf("不带 tail 应整体回放（旧行为）: %+v", res.reply)
	}
}

// 没有会话记录（不录制 / 无痕会话）时，被裁掉的输出就再也读不到了：必须整体回放。
func TestAttachTailNeedsRecording(t *testing.T) {
	e := newTestEnv(t, Limits{}) // 没有记录存储
	s, term := e.open(t)
	emitNumbered(t, s, term, 0, 5000)
	res := attachTail(t, s, &recSink{}, 1, 0, 300)
	if res.reply.From != 0 || res.reply.Truncated {
		t.Fatalf("没有记录时不能裁剪回放: %+v", res.reply)
	}
}

func TestAttachTailIgnoredOnResume(t *testing.T) {
	e := recorded(t)
	s, term := e.open(t)
	emitNumbered(t, s, term, 0, 5000)
	since := s.info().End - 1000
	sink := &recSink{}
	res := attachTail(t, s, sink, 1, since, 50)
	if res.reply.From != since || res.reply.Truncated || res.reply.Prefix != "" {
		t.Fatalf("续传必须从 since 接着发: %+v", res.reply)
	}
}

func TestAttachTailClampedToMax(t *testing.T) {
	e := recorded(t)
	s, term := e.open(t)
	emitNumbered(t, s, term, 0, 6000) // 约 480 KiB，行数多于 MaxTailLines
	res := attachTail(t, s, &recSink{}, 1, 0, 60000)
	if res.reply.From == 0 {
		t.Fatalf("超大的 tail 应按 %d 行截断，仍然裁剪: %+v", termproto.MaxTailLines, res.reply)
	}
}

func TestAttachTailDoesNotAffectAltScreen(t *testing.T) {
	e := recorded(t)
	s, term := e.open(t)
	emitNumbered(t, s, term, 0, 5000)
	altAt := s.info().End
	term.Emit([]byte("\x1b[?1049h\x1b[Halt-screen-content"))
	eventually(t, "进入备用屏", func() bool {
		s.mu.Lock() // readLoop 在持有 s.mu 时写 modes：测试读它也要加锁
		defer s.mu.Unlock()
		alt, _ := s.modes.Alt()
		return alt
	})
	res := attachTail(t, s, &recSink{}, 1, 0, 300)
	// 起点记在「进入备用屏」那条转义序列（ESC [ ? 1049 h，8 字节）之后。
	if res.reply.From < altAt || res.reply.From > altAt+8 {
		t.Fatalf("备用屏仍应只回放自进入备用屏以来的内容: from=%d altAt=%d", res.reply.From, altAt)
	}
	if res.reply.Prefix == "" {
		t.Fatalf("备用屏应发模式恢复序列")
	}
}

func TestAttachTailAfterRingWrapped(t *testing.T) {
	e := recorded(t)
	s, term := e.open(t)
	emitNumbered(t, s, term, 0, 20000) // 约 1.6 MB > 1 MiB 缓冲：已回绕
	if s.info().Base == 0 {
		t.Fatalf("测试前提：缓冲应已回绕: %+v", s.info())
	}
	sink := &recSink{}
	res := attachTail(t, s, sink, 1, 0, 200)
	r := res.reply
	if r.Truncated || r.From <= r.Base {
		t.Fatalf("回绕后同样应裁剪到末尾: %+v", r)
	}
	eventually(t, "回放完成", func() bool { return sink.outLen(1) == int(r.End-r.From) })
	got := sink.payload(t, 1, r.From)
	if got := bytes.Count(got, []byte("\n")); got != 200 {
		t.Fatalf("应回放 200 行，得到 %d", got)
	}

	// 不带 tail 的附着保持旧行为：从缓冲头整体回放，并标记「更早的输出已被覆盖」。
	old := attachTail(t, s, &recSink{}, 2, 0, 0)
	if !old.reply.Truncated || old.reply.From < old.reply.Base || old.reply.From > old.reply.Base+4096 {
		t.Fatalf("不带 tail 应整体回放并标记 truncated: %+v", old.reply)
	}
}
