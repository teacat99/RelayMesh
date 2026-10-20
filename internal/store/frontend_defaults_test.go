package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 前端需要一份出厂阶段模板和服务指引（首次渲染、「恢复默认」都要用），真源在 Go 端。
// 这份前端文件由本测试生成，平时只比对：任何一边改了默认值而没有同步，测试就会失败，
// 并提示用 UPDATE_FRONTEND_DEFAULTS=1 重新生成。
const frontendDefaultsPath = "../../frontend/src/lib/defaultPrompts.generated.ts"

func renderFrontendDefaults(t *testing.T) []byte {
	t.Helper()
	if strings.ContainsAny(DefaultServerInstructions, "`\\") || strings.Contains(DefaultServerInstructions, "${") {
		t.Fatal("默认服务指引里不能出现反引号、反斜杠或 ${，否则无法原样放进 TypeScript 模板字符串")
	}

	var phases bytes.Buffer
	enc := json.NewEncoder(&phases)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(model.DefaultPhaseTemplate()); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	out.WriteString("// 由 Go 端的默认值生成，请勿手改。\n")
	out.WriteString("// 重新生成：UPDATE_FRONTEND_DEFAULTS=1 go test ./internal/store -run TestFrontendDefaultsInSync\n\n")
	out.WriteString("export interface DefaultPhaseItem {\n  id: string\n  label: string\n  description: string\n  prompt: string\n}\n\n")
	out.WriteString("// 出厂阶段模板：讨论 / 方案 / 开发（model.DefaultPhaseTemplate）。\n")
	out.WriteString("export const DEFAULT_PHASE_TEMPLATE: DefaultPhaseItem[] = ")
	out.Write(bytes.TrimRight(phases.Bytes(), "\n"))
	out.WriteString("\n\n// 出厂的 MCP 服务指引（DefaultServerInstructions）。\n")
	out.WriteString("export const DEFAULT_SERVER_INSTRUCTIONS = `")
	out.WriteString(DefaultServerInstructions)
	out.WriteString("`\n")
	return out.Bytes()
}

func TestFrontendDefaultsInSync(t *testing.T) {
	want := renderFrontendDefaults(t)
	path := filepath.FromSlash(frontendDefaultsPath)
	if os.Getenv("UPDATE_FRONTEND_DEFAULTS") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("已重新生成 %s", path)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取前端默认值文件失败: %v（用 UPDATE_FRONTEND_DEFAULTS=1 生成）", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("前端的默认阶段模板 / 服务指引与 Go 端不一致；用 UPDATE_FRONTEND_DEFAULTS=1 go test ./internal/store -run TestFrontendDefaultsInSync 重新生成后一并提交")
	}
}
