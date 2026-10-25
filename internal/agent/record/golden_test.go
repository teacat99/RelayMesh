package record

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenEvents 是一段覆盖各种编码边界的事件序列：它的十六进制形式保存在 testdata/golden_events.hex，
// 前端的解码器（frontend/src/lib/terminal/record.ts）读取同一份文件做对照测试，
// 这样 Go 编码器与 TypeScript 解码器任何一方悄悄改了格式都会立刻失败。
func goldenEvents() []byte {
	var b []byte
	b = AppendEvent(b, KindMark, 0, encodeMark(Mark{T: MarkSeg, Seq: 3, T0: 1_700_000_000_123, Rows: 24, Cols: 80}))
	b = AppendEvent(b, KindOutput, 5, []byte("hello \x1b[31mworld\x1b[0m\r\n"))
	// 一个汉字的 UTF-8 编码被拆在两个事件里（e4 bd | a0），前端必须流式解码才不会出现乱码。
	b = AppendEvent(b, KindOutput, 130, []byte{0xe4, 0xbd})
	b = AppendEvent(b, KindOutput, 16_384, []byte{0xa0, 0xe5, 0xa5, 0xbd, '\n'}) // dt 需要 3 字节的变长编码
	b = AppendEvent(b, KindResize, 2, encodeResize(40, 120))
	b = AppendEvent(b, KindOutput, 0, []byte{}) // 空净荷
	b = AppendEvent(b, KindOutput, 1, []byte(strings.Repeat("x", 300)))
	b = AppendEvent(b, KindMark, 7, encodeMark(Mark{T: MarkGap, Reason: GapDiskLow, Bytes: 4096}))
	code := 3
	b = AppendEvent(b, KindMark, 900, encodeMark(Mark{T: MarkEnd, Reason: "exit", Code: &code}))
	return b
}

func TestGoldenEventsMatchFixture(t *testing.T) {
	got := hex.EncodeToString(goldenEvents()) + "\n"
	path := filepath.Join("testdata", "golden_events.hex")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("缺少对照文件 %s（用 UPDATE_GOLDEN=1 生成）: %v", path, err)
	}
	// 对照文件是文本：Windows 上 git 默认把检出的文本文件换成 CRLF（core.autocrlf=true，GitHub 的 Windows runner 就是这样），
	// 内容并没有变，所以比较前把行尾统一成 LF；真正的差异（十六进制内容不同）仍会被抓到。
	wantText := strings.ReplaceAll(string(want), "\r\n", "\n")
	if wantText != got {
		t.Fatalf("事件编码与对照文件不一致：格式被改动了吗？如果是有意的，请同步前端解码器并用 UPDATE_GOLDEN=1 重新生成。\nwant %s\n got %s", wantText, got)
	}

	// 解码回来要得到同一个序列。
	events, used, err := DecodeEvents(goldenEvents())
	if err != nil || used != len(goldenEvents()) || len(events) != 9 {
		t.Fatalf("解码: events=%d used=%d err=%v", len(events), used, err)
	}
	if events[3].DT != 16_384 || events[6].DT != 1 || len(events[6].Payload) != 300 {
		t.Fatalf("边界值不对: %+v", events[3])
	}
}
