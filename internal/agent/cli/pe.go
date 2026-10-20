package cli

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PE 可执行文件的「子系统」字段。控制台子系统的程序一启动就会拥有一个控制台窗口；
// 图形界面子系统的程序则没有——作为登录 / 开机自启的计划任务运行时，不会在桌面上闪出黑窗口。
//
// 我们不单独发布一个「无窗口版」：安装时把已安装的可执行文件复制一份并改写这一个字段，
// 效果等同于 `editbin /SUBSYSTEM:WINDOWS`。控制台版保留给 status / uninstall 等命令行操作。
const (
	peSubsystemGUI uint16 = 2
	peSubsystemCUI uint16 = 3
)

var errNotPE = errors.New("不是有效的 Windows 可执行文件（PE）")

// patchPESubsystem 原地改写 PE 映像的子系统字段，返回原值。只会修改这两个字节。
func patchPESubsystem(image []byte, subsystem uint16) (old uint16, err error) {
	if len(image) < 0x40 || image[0] != 'M' || image[1] != 'Z' {
		return 0, errNotPE
	}
	e := int(binary.LittleEndian.Uint32(image[0x3c:]))
	// PE 签名(4) + COFF 头(20) + 可选头里到 Subsystem 为止的 70 字节。
	if e < 0 || e > len(image)-(4+20+70) {
		return 0, errNotPE
	}
	if string(image[e:e+4]) != "PE\x00\x00" {
		return 0, errNotPE
	}
	opt := e + 4 + 20
	switch binary.LittleEndian.Uint16(image[opt:]) {
	case 0x10b, 0x20b: // PE32 / PE32+：Subsystem 都位于可选头偏移 68
	default:
		return 0, errNotPE
	}
	off := opt + 68
	old = binary.LittleEndian.Uint16(image[off:])
	binary.LittleEndian.PutUint16(image[off:], subsystem)
	return old, nil
}

// copyAsGUI 把 src 复制为 dst，并把子系统改为图形界面（原子写入，不会留下半截文件）。
func copyAsGUI(src, dst string) error {
	image, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if _, err := patchPESubsystem(image, peSubsystemGUI); err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(image); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		cleanup()
		return err
	}
	return nil
}
