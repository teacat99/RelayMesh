package terminal

import (
	"context"
	"fmt"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// SetGuardWakePermit 设置预设的唤醒许可（MCP / REST 共用）。
func (h *Hub) SetGuardWakePermit(ctx context.Context, presetID string, permit int, by, note string) (*store.GuardWakePermitState, error) {
	if h == nil || h.guardian == nil {
		return nil, fmt.Errorf("会话守护不可用")
	}
	return h.guardian.SetWakePermit(ctx, presetID, permit, by, note, h.now())
}
