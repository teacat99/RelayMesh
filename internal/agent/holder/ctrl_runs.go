package holder

import (
	"encoding/json"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// handleRun 处理模型执行相关的控制帧。本机没有启用模型执行（旧配置 / 测试）时给出明确的失败，而不是沉默。
func (l *ctrl) handleRun(t string, data []byte) {
	em := l.host.exec
	switch t {
	case termproto.TRun:
		var m termproto.Run
		if json.Unmarshal(data, &m) != nil {
			return
		}
		if em == nil {
			_ = l.sendJSON(termproto.RunFailed{T: termproto.TRunFailed, ID: m.ID, Reason: termproto.RunFailUnavailable})
			return
		}
		em.StartRun(m)
	case termproto.TRunCancel:
		var m termproto.RunCancel
		if json.Unmarshal(data, &m) == nil && em != nil {
			em.Cancel(m.ID, m.Reason)
		}
	case termproto.TRunAck:
		var m termproto.RunAck
		if json.Unmarshal(data, &m) == nil && em != nil {
			em.Ack(m.ID)
		}
	case termproto.TRunRead:
		var m termproto.RunRead
		if json.Unmarshal(data, &m) != nil {
			return
		}
		if em == nil {
			_ = l.sendJSON(termproto.RunReadFail{T: termproto.TRunReadFail, Req: m.Req, Reason: termproto.RunReadNotFound})
			return
		}
		em.ServeRead(l, m)
	case termproto.TRunsSync:
		if em != nil {
			em.Sync()
		}
	}
}
