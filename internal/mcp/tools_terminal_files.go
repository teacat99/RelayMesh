package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/termexec"
)

// terminal_files：在已授权主机上、令牌允许目录内查看（stat / hash）、拉取（pull_prepare）与写入（push_url）文件。
// 授权、路径校验与审计全部在 termexec.FileExecutor（Hub 与主机一侧）里完成，这里只做 MCP 协议适配。

// flexBool 宽容地解析布尔值：接受 true / false、"true" / "false" 与 0 / 1，因为不同的模型客户端序列化不一致。
type flexBool struct{ V bool }

func (f *flexBool) UnmarshalJSON(b []byte) error {
	switch strings.ToLower(strings.Trim(string(bytes.TrimSpace(b)), `"`)) {
	case "true", "1", "yes":
		f.V = true
	case "false", "0", "no", "", "null":
		f.V = false
	default:
		return fmt.Errorf("expected a boolean, got %s", string(b))
	}
	return nil
}

func (s *Server) handleTerminalFiles(ctx context.Context, credCtx *CredentialContext, raw json.RawMessage) (any, bool, error) {
	fe, ok := s.terminalExecutor().(termexec.FileExecutor)
	if !ok || fe == nil {
		return terminalOutcome(nil, &termexec.Error{Code: termexec.CodeFilesOff, Message: "文件能力在此服务上不可用"})
	}
	var args struct {
		Action    string   `json:"action"`
		Target    string   `json:"target"`
		Path      string   `json:"path"`
		URL       string   `json:"url"`
		SHA256    string   `json:"sha256"`
		Overwrite flexBool `json:"overwrite"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return terminalOutcome(nil, err)
	}
	res, err := fe.Files(ctx, s.terminalCaller(credCtx), termexec.FileRequest{
		Action: args.Action, Target: args.Target, Path: args.Path, URL: args.URL, SHA256: args.SHA256, Overwrite: args.Overwrite.V,
	})
	if err == nil {
		// 下载地址在 Hub 里只是相对路径；这里按本次请求的来源补全成可直接使用的绝对地址。
		if pull, isPull := res.(termexec.FilePullResult); isPull && credCtx != nil && credCtx.BaseURL != "" {
			pull.URL = strings.TrimRight(credCtx.BaseURL, "/") + pull.URL
			res = pull
		}
	}
	return terminalOutcome(res, err)
}
