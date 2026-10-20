//go:build windows

package agent

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 上的文件保护：私有 ACL = 不继承上级，只授予 SYSTEM、BUILTIN\Administrators 与当前用户完全控制。

const (
	sidSystem         = "S-1-5-18"
	sidAdministrators = "S-1-5-32-544"
)

// 这些主体代表「本机上的其他用户」：任何面向它们的读写类授权都意味着私钥可能被别人读走或改写。
var broadSIDs = []string{
	"S-1-1-0",      // Everyone
	"S-1-5-32-545", // BUILTIN\Users
	"S-1-5-11",     // Authenticated Users
	"S-1-5-32-546", // BUILTIN\Guests
	"S-1-5-7",      // Anonymous Logon
	"S-1-5-4",      // Interactive
	"S-1-5-2",      // Network
}

// 会让「别人读取或篡改私钥」成为可能的访问权限位。
const dangerousMask = windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
	windows.FILE_WRITE_EA | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER |
	windows.GENERIC_READ | windows.GENERIC_WRITE | windows.GENERIC_ALL

func currentUserSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tu.User.Sid, nil
}

// privateSDDL 生成私有 DACL 的 SDDL。目录的 ACE 带 OICI，使其中新建的文件 / 子目录继承同一份授权。
func privateSDDL(user *windows.SID, dir bool) string {
	flags := ""
	if dir {
		flags = "OICI"
	}
	var b strings.Builder
	b.WriteString("D:P")
	seen := map[string]bool{}
	for _, sid := range []string{sidSystem, sidAdministrators, user.String()} {
		if seen[sid] {
			continue
		}
		seen[sid] = true
		fmt.Fprintf(&b, "(A;%s;FA;;;%s)", flags, sid)
	}
	return b.String()
}

// applyPrivateACL 把 path 的 DACL 重置为私有（受保护，不继承上级）。
func applyPrivateACL(path string, dir bool) error {
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(privateSDDL(user, dir))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// verifyPrivateFile 核查属主与 ACL；属主是自己且 ACL 过宽时自动收紧。
func verifyPrivateFile(path string) error {
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("无法读取 %s 的访问控制信息: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("无法确定 %s 的属主: %v", path, err)
	}
	if !ownerAllowed(owner, me) {
		return fmt.Errorf("身份文件 %s 的属主（%s）既不是当前用户也不是管理员 / SYSTEM，拒绝使用", path, owner.String())
	}
	wide, err := hasBroadAccess(sd)
	if err != nil {
		return err
	}
	if !wide {
		return nil
	}
	if err := applyPrivateACL(path, false); err != nil {
		return fmt.Errorf("身份文件 %s 对其他本地用户可访问，且无法收紧: %w", path, err)
	}
	return nil
}

func ownerAllowed(owner, me *windows.SID) bool {
	if owner.Equals(me) {
		return true
	}
	for _, s := range []string{sidSystem, sidAdministrators} {
		if want, err := windows.StringToSid(s); err == nil && owner.Equals(want) {
			return true
		}
	}
	return false
}

// hasBroadAccess 判断 DACL 里是否有面向「其他本地用户」的读写类允许项。没有 DACL（NULL DACL）等于对所有人开放。
func hasBroadAccess(sd *windows.SECURITY_DESCRIPTOR) (bool, error) {
	dacl, _, err := sd.DACL()
	if err != nil {
		// NULL DACL：所有人完全访问。
		return true, nil
	}
	if dacl == nil {
		return true, nil
	}
	broad := make([]*windows.SID, 0, len(broadSIDs))
	for _, s := range broadSIDs {
		if sid, err := windows.StringToSid(s); err == nil {
			broad = append(broad, sid)
		}
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, fmt.Errorf("读取 ACL 条目失败: %w", err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		if ace.Mask&dangerousMask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		for _, b := range broad {
			if sid.Equals(b) {
				return true, nil
			}
		}
	}
	return false, nil
}
