//go:build !linux && !darwin && !windows

package record

import "errors"

// freeSpace 在不支持的平台上报告未知，调用方据此跳过低磁盘保护。
func freeSpace(string) (uint64, error) { return 0, errors.ErrUnsupported }
