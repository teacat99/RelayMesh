//go:build !linux && !darwin && !windows

package holder

// bootIdentity 在没有可靠开机标识的平台上返回空值；此时「主机重启」不会被宣称，只会报守护进程退出。
func bootIdentity() (id string, bootTime int64) { return "", 0 }
