package mcp

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 回归（#74）：长任务的使用契约要写进工具说明。
// 模型只读得到工具说明：不写明 7 天的上限，它就不知道能把超时设到多长；不写明「等待时间最多 120 秒、之后用编号查询」，
// 它就会反复重跑命令来「查看进度」；不写明主机只保留开头 8 MiB 与最后 256 KiB，它就会指望从捕获的输出里读到被丢掉的中间部分。
// 数字取自代码里的常量，说明与实现漂移时这里会失败。
func TestTerminalToolDescriptionsStateTheLongTaskContract(t *testing.T) {
	maxTimeout := strconv.Itoa(termproto.MaxRunTimeoutSec)

	t.Run("原生 terminal_exec", func(t *testing.T) {
		mustContainAll(t, "terminal_exec 说明", terminalExecDescription,
			maxTimeout, "7 days", "run_id", "terminal_run status", "wait_seconds up to 120", "adjusted",
			"8 MiB", "256 KiB", "dropped_bytes", "holder_stopped", "tail -n 200")
		props := schemaProps(t, terminalExecSchema(false))
		mustContainAll(t, "timeout_seconds 参数", props["timeout_seconds"], maxTimeout, "7 days", "adjusted", "independent of wait_seconds")
		mustContainAll(t, "wait_seconds 参数", props["wait_seconds"], "max 120", "adjusted", "keep the run_id")
		mustContainAll(t, "max_output_bytes 参数", props["max_output_bytes"], strconv.Itoa(termproto.MaxRunRead))
	})
	t.Run("原生 terminal_run", func(t *testing.T) {
		mustContainAll(t, "terminal_run 说明", terminalRunDescription, "wait_seconds=120", "8 MiB", "256 KiB", "gap")
	})
	t.Run("兼容 exec_start / exec", func(t *testing.T) {
		mustContainAll(t, "exec_start 说明", compatExecStartDescription,
			maxTimeout, "7 days", "job_id", "job_status", "wait_seconds up to 120", "adjusted", "8 MiB", "256 KiB", "holder_stopped", "tail -n 200")
		for _, start := range []bool{false, true} {
			props := schemaProps(t, compatExecSchema(start, false))
			mustContainAll(t, "兼容 timeout_seconds 参数（start="+strconv.FormatBool(start)+"）", props["timeout_seconds"], maxTimeout, "7 days")
		}
	})
	t.Run("说明里不含反引号（原样展示给模型 / 用户，不当 Markdown 渲染）", func(t *testing.T) {
		for name, text := range map[string]string{
			"terminal_exec": terminalExecDescription, "terminal_run": terminalRunDescription, "exec_start": compatExecStartDescription,
		} {
			if strings.ContainsRune(text, '`') {
				t.Errorf("%s 的说明里有反引号", name)
			}
		}
	})
}

// schemaProps 取出 JSON Schema 里每个属性的 description。
func schemaProps(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var s struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(s.Properties))
	for k, v := range s.Properties {
		out[k] = v.Description
	}
	return out
}

func mustContainAll(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("%s 缺少 %q:\n%s", what, w, text)
		}
	}
}
