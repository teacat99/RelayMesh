package cli

import (
	"os"
	"path/filepath"
	"sync"
)

// logFileMaxBytes 是单个日志文件的上限；超过后轮转为 <path>.1（只保留一代）。
// Agent 的日志很稀疏（连接 / 断开 / 重试），这个上限足够覆盖数周的持续故障，又不会悄悄占满磁盘。
const logFileMaxBytes = 4 << 20

// logFilePreamble 写在每个新建日志文件的开头。Windows 上设为 UTF-8 BOM：
// PowerShell 5.1 的 Get-Content 与旧版记事本没有 BOM 时会按系统 ANSI 代码页解读，中文日志会显示成乱码。
var logFilePreamble []byte

// rotatingFile 是按大小轮转的日志文件，并发安全。
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	max      int64
	preamble []byte
	f        *os.File
	size     int64
}

func openRotatingFile(path string, max int64) (*rotatingFile, error) {
	return openRotatingFileWith(path, max, logFilePreamble)
}

func openRotatingFileWith(path string, max int64, preamble []byte) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := &rotatingFile{path: path, max: max, preamble: preamble}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFile) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	if w.size == 0 && len(w.preamble) > 0 {
		n, err := f.Write(w.preamble)
		w.size += int64(n)
		if err != nil {
			_ = f.Close()
			return err
		}
	}
	return nil
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > int64(len(w.preamble)) && w.size+int64(len(p)) > w.max {
		_ = w.f.Close()
		_ = os.Remove(w.path + ".1")
		_ = os.Rename(w.path, w.path+".1")
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
