//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// setSDDL 用 SDDL 直接覆盖 path 的 DACL（受保护），用来构造「别人也能读」的场景。
func setSDDL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("SDDL %q: %v", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo(%s): %v", path, err)
	}
}

func broadAccess(t *testing.T, path string) bool {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	wide, err := hasBroadAccess(sd)
	if err != nil {
		t.Fatal(err)
	}
	return wide
}

func mySID(t *testing.T) string {
	t.Helper()
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	return sid.String()
}

func TestPrivateSDDLShape(t *testing.T) {
	me, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	file := privateSDDL(me, false)
	dir := privateSDDL(me, true)
	if !strings.HasPrefix(file, "D:P") || strings.Contains(file, "OICI") {
		t.Fatalf("文件的 SDDL 应受保护且不带继承标志: %s", file)
	}
	if !strings.Contains(dir, "(A;OICI;FA;;;S-1-5-18)") || !strings.Contains(dir, "(A;OICI;FA;;;S-1-5-32-544)") || !strings.Contains(dir, "S-1-5-21-1-2-3-1001") {
		t.Fatalf("目录的 SDDL 应包含 SYSTEM / 管理员 / 当前用户且可继承: %s", dir)
	}
	sys, _ := windows.StringToSid(sidSystem)
	if got := strings.Count(privateSDDL(sys, true), sidSystem); got != 1 {
		t.Fatalf("当前用户就是 SYSTEM 时不应重复授权: %d", got)
	}
}

func TestApplyPrivateACLRemovesBroadAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	setSDDL(t, path, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;BU)(A;;FA;;;"+mySID(t)+")")
	if !broadAccess(t, path) {
		t.Fatal("构造的 ACL 里 Users 有读权限，应被识别为过宽")
	}
	if err := applyPrivateACL(path, false); err != nil {
		t.Fatal(err)
	}
	if broadAccess(t, path) {
		t.Fatal("应用私有 ACL 后不应再有面向其他用户的授权")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "x" {
		t.Fatalf("收紧后本人仍应能读取: %v %q", err, b)
	}
}

func TestBroadAccessDetectsEveryoneAndIgnoresHarmlessMasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	me := mySID(t)
	// Everyone 只有「读取属性」这类无害权限：不算泄露。
	setSDDL(t, path, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;"+me+")(A;;0x80;;;WD)")
	if broadAccess(t, path) {
		t.Fatal("仅读取属性（FILE_READ_ATTRIBUTES）不应被判为过宽")
	}
	setSDDL(t, path, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;"+me+")(A;;FR;;;WD)")
	if !broadAccess(t, path) {
		t.Fatal("Everyone 可读应被判为过宽")
	}
	setSDDL(t, path, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;"+me+")(A;;FR;;;AU)")
	if !broadAccess(t, path) {
		t.Fatal("Authenticated Users 可读应被判为过宽")
	}
}

func TestVerifyPrivateFileHealsOwnFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.key")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	setSDDL(t, path, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FRFW;;;BU)(A;;FA;;;"+mySID(t)+")")
	if err := checkKeyFileSecurity(path); err != nil {
		t.Fatalf("属主是自己时应自动收紧而不是报错: %v", err)
	}
	if broadAccess(t, path) {
		t.Fatal("检查之后文件应已被收紧")
	}
}

func TestCheckKeyFileSecurityMissingAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	if err := checkKeyFileSecurity(filepath.Join(dir, "nope")); err != nil {
		t.Fatalf("文件不存在应交给调用方处理: %v", err)
	}
	if err := checkKeyFileSecurity(dir); err == nil {
		t.Fatal("目录不是普通文件，应被拒绝")
	}
}

func TestCreateIdentityIsPrivateEvenUnderOpenParent(t *testing.T) {
	// 模拟 %ProgramData% 下的默认情形：上级目录对所有本地用户可读，新建子目录会继承这份授权。
	parent := t.TempDir()
	setSDDL(t, parent, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;"+mySID(t)+")(A;OICI;FR;;;BU)(A;OICI;FR;;;WD)")
	dataDir := filepath.Join(parent, "RelayMesh", "Agent")
	id, err := LoadOrCreateIdentity(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if broadAccess(t, filepath.Join(parent, "RelayMesh", "Agent")) {
		t.Fatal("数据目录应已收紧为私有")
	}
	if broadAccess(t, id.Path) {
		t.Fatal("身份文件从创建起就应是私有的")
	}
	// 再次加载应通过复核并得到同一个机器码。
	again, err := LoadIdentity(dataDir)
	if err != nil || again.MachineCode() != id.MachineCode() {
		t.Fatalf("重新加载失败: %v", err)
	}
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

func TestTrustStoreFileIsPrivate(t *testing.T) {
	parent := t.TempDir()
	setSDDL(t, parent, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;"+mySID(t)+")(A;OICI;FR;;;WD)")
	path := filepath.Join(parent, "known_servers")
	if err := writeFileAtomic(path, []byte("host fp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if broadAccess(t, path) {
		t.Fatal("已信任服务器记录也应是私有的：它被改写就等于信任被劫持")
	}
}
