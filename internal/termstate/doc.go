// Package termstate 是终端会话「输出流状态」的共享实现：按累计偏移寻址的环形缓冲（Ring）与 DEC 私有模式跟踪器（ModeTracker）。
//
// 它们原本属于 Agent 侧的会话守护进程；Hub 为「直连的 SSH 主机」提供人工终端（Hub 本地会话，见 internal/terminal/localterm.go）时
// 需要完全相同的重连续传与备用屏恢复行为，所以抽成这个不依赖任何 PTY / 网络的小包，两条路径共用同一份实现与测试。
package termstate
