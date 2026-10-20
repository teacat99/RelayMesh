package cli

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// makePE 构造一个只含 DOS 头 + PE 签名 + COFF 头 + 可选头（到 Subsystem 为止）的最小映像。
func makePE(magic, subsystem uint16) []byte {
	const e = 0x80
	img := make([]byte, 0x200)
	img[0], img[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(img[0x3c:], e)
	copy(img[e:], "PE\x00\x00")
	opt := e + 4 + 20
	binary.LittleEndian.PutUint16(img[opt:], magic)
	binary.LittleEndian.PutUint16(img[opt+68:], subsystem)
	// 放一些标记字节，用来确认除 Subsystem 之外什么都没被改动。
	for i := opt + 70; i < len(img); i++ {
		img[i] = byte(i)
	}
	return img
}

func TestPatchPESubsystem(t *testing.T) {
	for _, magic := range []uint16{0x10b, 0x20b} {
		img := makePE(magic, peSubsystemCUI)
		orig := append([]byte(nil), img...)
		old, err := patchPESubsystem(img, peSubsystemGUI)
		if err != nil || old != peSubsystemCUI {
			t.Fatalf("magic %#x: old=%d err=%v", magic, old, err)
		}
		off := 0x80 + 4 + 20 + 68
		if got := binary.LittleEndian.Uint16(img[off:]); got != peSubsystemGUI {
			t.Fatalf("Subsystem 应为 GUI: %d", got)
		}
		img[off], img[off+1] = orig[off], orig[off+1]
		if !bytes.Equal(img, orig) {
			t.Fatal("除 Subsystem 两个字节外不应有任何改动")
		}
	}
}

func TestPatchPESubsystemIsIdempotent(t *testing.T) {
	img := makePE(0x20b, peSubsystemGUI)
	old, err := patchPESubsystem(img, peSubsystemGUI)
	if err != nil || old != peSubsystemGUI {
		t.Fatalf("已经是 GUI 时应原样通过: old=%d err=%v", old, err)
	}
}

func TestPatchPESubsystemRejectsMalformedImages(t *testing.T) {
	good := makePE(0x20b, peSubsystemCUI)
	cases := map[string][]byte{
		"空":     nil,
		"太短":    []byte("MZ"),
		"不是 MZ": append([]byte("ELF"), good[3:]...),
		"e_lfanew 越界": func() []byte {
			b := append([]byte(nil), good...)
			binary.LittleEndian.PutUint32(b[0x3c:], 0xFFFFFFF0)
			return b
		}(),
		"e_lfanew 贴尾": func() []byte {
			b := append([]byte(nil), good...)
			binary.LittleEndian.PutUint32(b[0x3c:], uint32(len(b)-10))
			return b
		}(),
		"PE 签名错": func() []byte { b := append([]byte(nil), good...); b[0x80] = 'X'; return b }(),
		"可选头魔数错": makePE(0x1234, peSubsystemCUI),
	}
	for name, img := range cases {
		if _, err := patchPESubsystem(img, peSubsystemGUI); err == nil {
			t.Errorf("%s：应被拒绝", name)
		}
	}
}

func TestCopyAsGUI(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.exe")
	if err := os.WriteFile(src, makePE(0x20b, peSubsystemCUI), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sub", "aw.exe")
	if err := copyAsGUI(src, dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if binary.LittleEndian.Uint16(got[0x80+4+20+68:]) != peSubsystemGUI {
		t.Fatal("副本应为 GUI 子系统")
	}
	orig, _ := os.ReadFile(src)
	if binary.LittleEndian.Uint16(orig[0x80+4+20+68:]) != peSubsystemCUI {
		t.Fatal("源文件不能被改动")
	}
	entries, _ := os.ReadDir(filepath.Dir(dst))
	if len(entries) != 1 {
		t.Fatalf("不应残留临时文件: %v", entries)
	}
}

func TestCopyAsGUIRejectsNonPEAndLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "not-pe")
	if err := os.WriteFile(src, []byte("\x7fELF....."), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out", "x.exe")
	if err := copyAsGUI(src, dst); err == nil {
		t.Fatal("非 PE 文件应被拒绝")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("失败后不应留下目标文件")
	}
}
