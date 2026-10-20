//go:build windows

package files

// Windows 没有 O_NONBLOCK 的概念；设备名（CON、NUL 等）在 os.Root 打开时就会被拒绝。
const openNoBlock = 0
