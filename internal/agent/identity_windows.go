//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
)

// checkKeyFileSecurity 在读取私钥之前核查文件的归属与访问控制（对应 Unix 上的属主 + 0600 检查）：
//
//   - 必须是普通文件（拒绝符号链接 / 重解析点）；
//   - 属主只能是当前用户、BUILTIN\Administrators 或 SYSTEM——否则别人可能预先放置了一个自己持有私钥的身份文件；
//   - 不得有面向 Everyone / Users / Authenticated Users / Guests / Anonymous 的读写类授权。
//     文件属主是自己时直接重置为私有 ACL 并继续（与 Unix 上自动 chmod 600 一致）。
//
// 不做 DPAPI：私钥与 Agent 进程运行在同一账户下，DPAPI 防不住同账户内的窃取，而跨账户已由 ACL 隔离。
func checkKeyFileSecurity(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("身份文件 %s 不是普通文件（拒绝跟随符号链接或特殊文件）", path)
	}
	return verifyPrivateFile(path)
}

// hardenDir 把数据目录收紧为私有（不继承上级，只授予 SYSTEM / 管理员 / 当前用户，新建的文件会继承）。
func hardenDir(dir string) error { return applyPrivateACL(dir, true) }

// restrictFile 给刚创建的临时文件设置私有 ACL；必须在写入内容之前调用。
func restrictFile(_ *os.File, path string, _ os.FileMode) error {
	if err := applyPrivateACL(path, false); err != nil {
		return fmt.Errorf("无法把 %s 设为仅本人与管理员可访问: %w", path, err)
	}
	return nil
}
