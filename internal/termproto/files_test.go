package termproto

import (
	"bytes"
	"strings"
	"testing"
)

func TestFileDataFrameRoundTrip(t *testing.T) {
	in := FileChunk{Req: 7, Flags: FileFlagEOF | FileFlagLast, Off: 1 << 33, Total: 1<<33 + 5, Data: []byte("hello")}
	got, err := DecodeFileData(EncodeFileData(in))
	if err != nil {
		t.Fatal(err)
	}
	if got.Req != in.Req || got.Flags != in.Flags || got.Off != in.Off || got.Total != in.Total || !bytes.Equal(got.Data, in.Data) {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestFileDataFrameRejectsBadInput(t *testing.T) {
	if _, err := DecodeFileData([]byte{FrameFileData, 0, 0}); err != ErrShortFrame {
		t.Fatalf("short frame: %v", err)
	}
	ok := EncodeFileData(FileChunk{Req: 1, Data: []byte("x")})
	ok[0] = FrameRunData
	if _, err := DecodeFileData(ok); err != ErrWrongFrame {
		t.Fatalf("wrong type: %v", err)
	}
	big := EncodeFileData(FileChunk{Req: 1, Data: make([]byte, MaxFileDataFrame+1)})
	if _, err := DecodeFileData(big); err != ErrPayloadTooBig {
		t.Fatalf("oversize: %v", err)
	}
}

func TestFileFrameTypeDoesNotCollide(t *testing.T) {
	seen := map[byte]string{
		FrameOutput: "output", FrameInput: "input", FrameAck: "ack", FrameRecord: "record",
		FrameRunOut: "run_out", FrameRunData: "run_data",
	}
	if name, dup := seen[FrameFileData]; dup {
		t.Fatalf("FrameFileData 与 %s 帧类型冲突", name)
	}
}

func TestFileReqProblem(t *testing.T) {
	good := func(r FileReq) FileReq { r.Roots = []string{"/srv/data"}; return r }
	hexHandle := strings.Repeat("a", 32)
	cases := []struct {
		name string
		req  FileReq
		bad  bool
	}{
		{"stat ok", good(FileReq{Op: FileOpStat, Path: "/srv/data/a"}), false},
		{"unknown op", good(FileReq{Op: "rm", Path: "/x"}), true},
		{"stat without path", good(FileReq{Op: FileOpStat}), true},
		{"read ok", good(FileReq{Op: FileOpRead, Handle: hexHandle, Len: 10}), false},
		{"read bad handle", good(FileReq{Op: FileOpRead, Handle: "../../etc"}), true},
		{"push ok", good(FileReq{Op: FileOpPushURL, Path: "/srv/data/a", URL: "https://example.com/a", SHA256: strings.Repeat("0", 64)}), false},
		{"push missing url", good(FileReq{Op: FileOpPushURL, Path: "/srv/data/a"}), true},
		{"push bad sha", good(FileReq{Op: FileOpPushURL, Path: "/a", URL: "https://x/", SHA256: "ABC"}), true},
		{"nul in path", good(FileReq{Op: FileOpStat, Path: "/srv/a\x00b"}), true},
		{"newline in path", good(FileReq{Op: FileOpStat, Path: "/srv/a\nb"}), true},
		{"invalid utf8", good(FileReq{Op: FileOpStat, Path: "/srv/\xff"}), true},
		{"long path", good(FileReq{Op: FileOpStat, Path: "/" + strings.Repeat("a", MaxFilePathBytes)}), true},
		{"too many roots", FileReq{Op: FileOpStat, Path: "/a", Roots: make([]string, MaxFileRoots+1)}, true},
		{"control char in root", FileReq{Op: FileOpStat, Path: "/a", Roots: []string{"/srv\r"}}, true},
		{"whole file system sentinel", FileReq{Op: FileOpStat, Path: "/etc/hosts", Roots: []string{FileRootAll}}, false},
		{"negative max", good(FileReq{Op: FileOpHash, Path: "/a", Max: -1}), true},
		{"long url", good(FileReq{Op: FileOpPushURL, Path: "/a", URL: "https://x/" + strings.Repeat("a", MaxFileURLBytes)}), true},
	}
	for _, c := range cases {
		if got := c.req.Problem(); (got != "") != c.bad {
			t.Errorf("%s: Problem() = %q, 期望有问题 = %v", c.name, got, c.bad)
		}
	}
}

func TestFileResSanitized(t *testing.T) {
	// 失败：未知错误码回落、消息被净化并限长
	f := FileRes{Req: 3, OK: false, Code: "<script>", Msg: "bad\x00\u202e thing " + strings.Repeat("x", 500)}.Sanitized()
	if f.OK || f.Code != FileErrIO || strings.ContainsAny(f.Msg, "\x00\u202e") || len([]rune(f.Msg)) > 200 {
		t.Fatalf("failure not sanitized: %+v", f)
	}
	if k := (FileRes{Code: FileErrNotAllowed}).Sanitized(); k.Code != FileErrNotAllowed {
		t.Fatalf("known code changed: %+v", k)
	}
	// 成功：非法的句柄 / 摘要 / 状态码 / 模式被丢弃
	ok := FileRes{OK: true, Req: 9, SHA256: "xyz", Handle: "../h", Status: 99999, Redirects: 1 << 20,
		Stat: &FileStat{Exists: true, Type: "device", Size: -5, Mode: "rwx", MTime: -1}, Source: "https://a.example\x1b[31m"}.Sanitized()
	if ok.SHA256 != "" || ok.Handle != "" || ok.Status != 0 || ok.Redirects != 0 {
		t.Fatalf("invalid scalar kept: %+v", ok)
	}
	if ok.Stat == nil || ok.Stat.Type != FileTypeOther || ok.Stat.Size != 0 || ok.Stat.Mode != "" || ok.Stat.MTime != 0 {
		t.Fatalf("stat not clamped: %+v", ok.Stat)
	}
	if strings.ContainsRune(ok.Source, 0x1b) {
		t.Fatalf("source kept control char: %q", ok.Source)
	}
	good := FileRes{OK: true, SHA256: strings.Repeat("ab", 32), Handle: strings.Repeat("0", 32), Status: 200,
		Stat: &FileStat{Exists: true, Type: FileTypeFile, Size: 12, Mode: "0644", MTime: 1700000000}}.Sanitized()
	if good.SHA256 == "" || good.Handle == "" || good.Status != 200 || good.Stat.Mode != "0644" || good.Stat.Type != FileTypeFile {
		t.Fatalf("valid fields dropped: %+v", good)
	}
	// 不存在：不带其它字段
	none := FileRes{OK: true, Stat: &FileStat{Exists: false, Type: FileTypeFile, Size: 4}}.Sanitized()
	if none.Stat == nil || none.Stat.Exists || none.Stat.Type != "" || none.Stat.Size != 0 {
		t.Fatalf("nonexistent stat leaked fields: %+v", none.Stat)
	}
}

func TestRootProblem(t *testing.T) {
	ok := []string{
		"/srv/data", "/srv/data/", "/srv//data/./x", "/home/me/my dir", "~", "~/work", `~\work`,
		`C:\Data`, `c:/data/x`, `D:\a\b\c`, "/a/b.c/d", `/my\odd`,
	}
	for _, p := range ok {
		if msg := RootProblem(p); msg != "" {
			t.Errorf("%q 应合法，实际 %q", p, msg)
		}
	}
	bad := []string{
		"", "relative/path", "./x", "x", "~root", "~root/x", "/", "//", "/.", "/./.", `\\server\share`, "//host/share",
		`C:`, `C:\`, `c:/`, `C:\.`,
		// 「整个文件系统」的哨兵不是合法的目录写法：人工输入的允许目录永远构造不出它
		FileRootAll, "*/", "**", "*x", " *",
		"/a/../b", "/a/..", "..", "/srv/data/../../etc", `C:\a\..\b`, `C:\a:stream`, `C:\a.`, `C:\a \b`,
		"/a\x00b", "/a\nb", "/a\tb", "/caf\xff", "/" + strings.Repeat("a", MaxFilePathBytes),
	}
	for _, p := range bad {
		if msg := RootProblem(p); msg == "" {
			t.Errorf("%q 应被拒绝", p)
		}
	}
}
