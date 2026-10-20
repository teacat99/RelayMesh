package record

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 按行数限高（Options.SessionLines / Policy.SessionLines）：超出后滚动丢弃最旧的分段，和字节上限互相独立。

func countLF(b []byte) int { return bytes.Count(b, []byte("\n")) }

// writeLines 写入 n 行；一半逐行写，一半把多行拼成一个大块写（覆盖单次输出被切成多个事件的路径）。
//
// 写入端是不阻塞的：写入队列（1024 项）满了就丢弃并记缺口。生产环境里这是对的（宁可丢输出也不能拖慢终端），
// 但测试要的是「写进去的每一行都在」，所以每写一批就 Flush 一次，让后台协程把队列排空——否则机器忙（并行跑
// 全量测试）或只有一个 P（GOMAXPROCS=1）时，后台协程来不及消费，队列溢出，测试会随机出现「缺口」。
func writeLines(w *Writer, from, n int, all *bytes.Buffer) {
	i := from
	end := from + n
	calls := 0
	out := func(b []byte) {
		w.Output(b)
		calls++
		if calls%200 == 0 {
			w.Flush()
		}
	}
	for i < end {
		if (i/1000)%2 == 0 {
			line := fmt.Sprintf("row %06d\r\n", i)
			all.WriteString(line)
			out([]byte(line))
			i++
			continue
		}
		var blob strings.Builder
		for k := 0; k < 700 && i < end; k++ {
			blob.WriteString(fmt.Sprintf("row %06d\r\n", i))
			i++
		}
		all.WriteString(blob.String())
		out([]byte(blob.String()))
	}
}

func TestSessionLinesCapDropsOldestSegments(t *testing.T) {
	const capLines = 5000
	s := newTestStore(t, func(o *Options) { o.SessionLines = capLines })
	sid := testSID(41)
	w, _ := s.Begin(Meta{SID: sid})
	var all bytes.Buffer
	writeLines(w, 0, 30000, &all)
	w.End(nil, "", "exit")

	info, err := s.Info(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Gap || !info.Head || info.DroppedBytes <= 0 {
		t.Fatalf("按行数丢弃之后应当标出缺口与「头部已被清理」: %+v", info)
	}
	// 输出总共才约 400 KB，远没到 1 MiB 的分段大小：能滚动，说明分段是被行数上限提前封存的。
	if info.RawBytes >= int64(DefaultSegmentBytes) {
		t.Fatalf("保留量 %d 字节：本用例不应靠字节上限触发", info.RawBytes)
	}
	if info.Segments[0].Seq <= 1 {
		t.Fatalf("最旧的分段应已被丢弃: %+v", info.Segments)
	}
	_, out := readAll(t, s, sid)
	if !bytes.HasSuffix(all.Bytes(), out) {
		t.Fatalf("保留下来的输出必须是整段输出的后缀（%d 字节）", len(out))
	}
	// 最近 capLines 行一定保留；多保留的不超过一个分段（行数分段约为上限的十分之一，再加最后一次大块写入的零头）。
	chunk := int(lineChunk(capLines))
	got := countLF(out)
	if got < capLines || got > capLines+chunk+800 {
		t.Fatalf("保留 %d 行，应在 [%d, %d] 之内", got, capLines, capLines+chunk+800)
	}
	if !strings.HasSuffix(string(out), "row 029999\r\n") {
		t.Fatalf("最新一行必须还在")
	}
}

func TestSessionLinesNotSetKeepsEverything(t *testing.T) {
	s := newTestStore(t, nil)
	sid := testSID(42)
	w, _ := s.Begin(Meta{SID: sid})
	var all bytes.Buffer
	writeLines(w, 0, 30000, &all)
	w.End(nil, "", "exit")
	info, err := s.Info(sid)
	if err != nil {
		t.Fatal(err)
	}
	if info.Gap || info.Head || info.DroppedBytes != 0 {
		t.Fatalf("没设行数上限时不应丢任何内容: %+v", info)
	}
	_, out := readAll(t, s, sid)
	if !bytes.Equal(out, all.Bytes()) {
		t.Fatalf("输出应完整保留：得到 %d 字节，应为 %d", len(out), all.Len())
	}
}

// 本机配置与服务端策略都给了行数上限时取更严的一方；服务端永远不能放宽本机的上限。
func TestSessionLinesEffectiveLimitIsStricterOfLocalAndPolicy(t *testing.T) {
	cases := []struct {
		name          string
		local, policy int64
		want          int64
	}{
		{"都没设", 0, 0, 0},
		{"只有本机", 20000, 0, 20000},
		{"只有策略", 0, 7000, 7000},
		{"策略更严", 20000, 5000, 5000},
		{"策略更松（不能放宽）", 5000, 50000, 5000},
		{"相等", 8000, 8000, 8000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t, func(o *Options) { o.SessionLines = c.local })
			s.SetPolicy(Policy{SessionLines: c.policy})
			if got := s.SessionLineLimit(); got != c.want {
				t.Fatalf("生效上限 = %d，应为 %d", got, c.want)
			}
			// 撤销策略后回到本机配置。
			s.SetPolicy(Policy{})
			if got := s.SessionLineLimit(); got != c.local {
				t.Fatalf("撤销策略后 = %d，应回到本机配置 %d", got, c.local)
			}
		})
	}
	// 负数的本机配置被当作不限制。
	s := newTestStore(t, func(o *Options) { o.SessionLines = -5 })
	if got := s.SessionLineLimit(); got != 0 {
		t.Fatalf("负数配置应视为不限制，得到 %d", got)
	}
}

// 会话正在录制时，服务端策略把行数上限收紧：马上按新上限收缩已保留的内容，不用等下一个分段封存。
func TestSessionLinesPolicyTightensLiveWriter(t *testing.T) {
	s := newTestStore(t, func(o *Options) { o.SegmentBytes = 16 << 10 })
	sid := testSID(43)
	w, _ := s.Begin(Meta{SID: sid})
	var all bytes.Buffer
	writeLines(w, 0, 30000, &all)
	w.Flush()
	before, err := s.Info(sid)
	if err != nil || before.Gap {
		t.Fatalf("收紧前不应有缺口: %+v err=%v", before, err)
	}

	s.SetPolicy(Policy{SessionLines: 5000})
	deadline := time.Now().Add(3 * time.Second)
	var after *Info
	for {
		after, err = s.Info(sid)
		if err != nil {
			t.Fatal(err)
		}
		if after.Gap && after.RawBytes < before.RawBytes {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("收紧后保留量没有缩小：%d → %d（%+v）", before.RawBytes, after.RawBytes, after)
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.End(nil, "", "exit")
	_, out := readAll(t, s, sid)
	if !bytes.HasSuffix(all.Bytes(), out) {
		t.Fatalf("保留下来的输出必须是整段输出的后缀")
	}
	// 分段按 16 KiB 封存（每段约 1000~1500 行）：最近 5000 行一定在，多出来的不超过一个分段。
	if got := countLF(out); got < 5000 || got > 5000+2000 {
		t.Fatalf("收紧后保留 %d 行，应在 [5000, 7000] 之内", got)
	}

	// 放宽策略（取消上限）不会把已丢弃的内容找回来。
	s.SetPolicy(Policy{})
	again, err := s.Info(sid)
	if err != nil || again.RawBytes > after.RawBytes+int64(1<<10) {
		t.Fatalf("取消上限不应让保留量变多: %+v", again)
	}
}

// 行数上限与字节上限互相独立：字节上限更小时先按字节丢。
func TestSessionLinesAndBytesQuotaBothApply(t *testing.T) {
	s := newTestStore(t, func(o *Options) {
		o.SessionLines = 1_000_000 // 行数上限很大，不会触发
		o.SegmentBytes = 8 << 10
		o.SessionBytes = 40 << 10
	})
	sid := testSID(44)
	w, _ := s.Begin(Meta{SID: sid})
	var all bytes.Buffer
	writeLines(w, 0, 6000, &all)
	w.End(nil, "", "exit")
	info, err := s.Info(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Gap || info.RawBytes > 40<<10 {
		t.Fatalf("字节上限应照常生效: %+v", info)
	}
}
