package agent

import (
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func TestDescribeCloseGivesActionableHints(t *testing.T) {
	cases := []struct {
		code websocket.StatusCode
		want string
	}{
		{websocket.StatusGoingAway, "重启"},
		{termproto.CloseInvalidHandshake, "版本不兼容"},
		{termproto.CloseGateClosed, "终端功能已关闭"},
		{termproto.CloseUnknownMachine, "发现窗口"},
		{termproto.CloseDisabled, "重新启用"},
		{termproto.CloseSuperseded, "同一份身份"},
		{termproto.CloseRateLimited, "限流"},
	}
	for _, c := range cases {
		got := describeClose(c.code)
		if !strings.Contains(got, c.want) {
			t.Errorf("code %d: %q 应包含 %q", int(c.code), got, c.want)
		}
		if !strings.Contains(got, "服务端关闭了连接（") {
			t.Errorf("code %d: %q 应保留关闭码，便于搜索日志", int(c.code), got)
		}
	}
	if got := describeClose(websocket.StatusCode(4999)); got != "服务端关闭了连接（4999）" {
		t.Errorf("未知关闭码应只给出数字，实际 %q", got)
	}
}
