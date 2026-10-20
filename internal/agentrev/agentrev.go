// Package agentrev 计算「Agent 侧代码指纹」（AgentRev）。
//
// 背景（#124 / #143）：整个仓库只有一个构建版本号，服务端每热更一次它就变一次；而 Agent 二进制只依赖少数几个包，
// 这些包没有变化时，不同版本号的 Agent 行为完全一样。拿版本号判断「可升级」，就会在每次服务端热更后让所有主机亮灯，
// 升级之后也看不出守护进程是否需要重启。
//
// 指纹是这些包的非测试源文件加上 go.mod / go.sum 的内容哈希：Agent 侧的代码真的变了，它才会变。
// 构建 Agent 时由 -X main.rev 注入；分发目录里的 REV 文件记录同一个值，服务端据此判断「可升级」。
//
// 设计约束：
//   - 只看文件内容与相对路径，不依赖 git、不依赖构建机的 Go 版本，所以在 Docker 构建上下文（没有 .git）里也能得到同一个值；
//   - 换行统一成 \n 再哈希，Windows 检出（CRLF）与 Linux 检出得到同一个值；
//   - 目录清单是静态的，覆盖是否完整由测试用 go list 在全部发布平台上核对（见 agentrev_test.go），清单漏项会让测试失败。
package agentrev

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Length 是指纹的十六进制长度。
const Length = 12

// Dirs 是 Agent 二进制（cmd/relaymesh-agent）在所有发布平台上依赖的、本模块内的包目录（相对模块根，不含子目录）。
// Agent 新增依赖而没有更新这份清单时，agentrev_test.go 里的覆盖测试会失败——这是它唯一的防漏机制，不要绕过。
var Dirs = []string{
	"cmd/relaymesh-agent",
	"internal/agent",
	"internal/agent/cli",
	"internal/agent/files",
	"internal/agent/holder",
	"internal/agent/ptyhost",
	"internal/agent/record",
	"internal/agent/runlog",
	"internal/agent/sshcred",
	"internal/relsign",
	"internal/relsign/trust",
	"internal/secure",
	"internal/sshexec",
	"internal/termproto",
	"internal/termstate",
}

// modFiles 是一并计入指纹的模块文件：依赖版本变了，二进制行为就可能变。
var modFiles = []string{"go.mod", "go.sum"}

// Compute 计算模块根 root 下的 Agent 侧代码指纹（Length 位小写十六进制）。
func Compute(root string) (string, error) { return ComputeDirs(root, Dirs) }

// ComputeDirs 与 Compute 相同，但目录清单由调用方给出（测试用）。
func ComputeDirs(root string, dirs []string) (string, error) {
	files, err := Files(root, dirs)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		// 「路径 NUL 长度 NUL 内容」：不同文件的内容不可能拼成另一种切分方式而得到同一个哈希。
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:Length], nil
}

// Files 返回参与指纹的文件（相对模块根、斜杠分隔、按路径排序）：go.mod / go.sum，以及各目录下的非测试 .go 文件。
// 所有平台的文件都计入（含 _windows.go 之类），所以同一份源码在任何平台上算出同一个指纹。
func Files(root string, dirs []string) ([]string, error) {
	if st, err := os.Stat(filepath.Join(root, "go.mod")); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("agentrev: %s 不是模块根目录（找不到 go.mod）", root)
	}
	var out []string
	for _, f := range modFiles {
		// go.sum 在没有任何依赖的模块里可以不存在。
		if st, err := os.Stat(filepath.Join(root, f)); err == nil && st.Mode().IsRegular() {
			out = append(out, f)
		}
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(d)))
		if err != nil {
			return nil, fmt.Errorf("agentrev: 读取目录 %s 失败: %w", d, err)
		}
		n := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			out = append(out, d+"/"+name)
			n++
		}
		if n == 0 {
			return nil, errors.New("agentrev: 目录 " + d + " 里没有任何非测试的 .go 文件")
		}
	}
	sort.Strings(out)
	return out, nil
}
