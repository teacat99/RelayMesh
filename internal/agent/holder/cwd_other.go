//go:build !linux

package holder

// processCwd 在没有廉价获取方式的平台上返回空串（「在原目录重建会话」据此降级为默认目录）。
func processCwd(int) string { return "" }
