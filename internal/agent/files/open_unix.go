//go:build !windows

package files

import "syscall"

// openNoBlock 让 open(2) 不阻塞：对没有写端的 FIFO 以只读方式打开会一直等下去，
// 加上 O_NONBLOCK 就能立刻返回，随后由类型检查把它拒绝掉。
const openNoBlock = syscall.O_NONBLOCK
