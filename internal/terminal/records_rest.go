package terminal

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// recordAuditWindow 是「连续读取」的判定间隔：同一个人读取同一个会话，距上一次读取不足这个时间，
// 就视为同一次查看，不再重复记审计。
const recordAuditWindow = 2 * time.Minute

// recordAuditMax 限制审计去重表的大小（主要防御异常客户端用海量不同会话号撑大内存）。
const recordAuditMax = 4096

// recordAuditGate 把连续的读取合并成一条审计。每次读取都会刷新时间，所以持续翻页或跟随最新的长时间查看只记一条。
type recordAuditGate struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// first 报告这次读取是否是一次新的查看（需要记审计），并刷新该键的最近读取时间。
func (g *recordAuditGate) first(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.last == nil {
		g.last = make(map[string]time.Time)
	}
	prev, seen := g.last[key]
	g.last[key] = now
	if len(g.last) > recordAuditMax {
		for k, t := range g.last {
			if now.Sub(t) > recordAuditWindow {
				delete(g.last, k)
			}
		}
		// 仍然超限（极端情况）：整体重置，宁可多记几条审计，也不让内存无限增长。
		if len(g.last) > recordAuditMax {
			g.last = map[string]time.Time{key: now}
		}
	}
	return !seen || now.Sub(prev) > recordAuditWindow
}

// ---------- 终端记录的 REST 接口 ----------
//
// 记录内容只存在主机本地，Hub 在这里只做三件事：返回会话索引（来自数据库、不含内容）、
// 把「读取记录」请求转给在线 Agent 并把内容原样流给人类浏览器、以及维护主机级的记录开关。
// 任何一处都不会把终端内容写入数据库或审计日志。

// handleHistory 返回一台主机的已结束会话索引（登录即可查看，离线主机也可用）。
func (h *Hub) handleHistory(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	rows, err := h.store.ListTerminalSessions(c.Request.Context(), code, true, limit)
	if err != nil {
		h.fail(c, err)
		return
	}
	online := false
	if a := h.onlineAgent(code); a != nil && a.isEnabled() {
		online = true
	}
	c.JSON(http.StatusOK, gin.H{"online": online, "sessions": rows})
}

// recordError 把记录读取错误映射成 HTTP 响应。
func (h *Hub) recordError(c *gin.Context, code, sid string, err error) {
	switch {
	case errors.Is(err, errRecordNotFound):
		if sid != "" {
			if e := h.store.MarkTerminalSessionPurged(c.Request.Context(), code, sid); e != nil {
				h.fail(c, e)
				return
			}
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "主机上已没有这个会话的记录（可能已按保留策略清理）", "code": "record_gone"})
	case errors.Is(err, errRecordBusy), errors.Is(err, errRecordTooMany):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "主机正忙，请稍后重试", "code": "busy"})
	case errors.Is(err, errRecordTimeout):
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "主机未在规定时间内响应", "code": "agent_timeout"})
	case errors.Is(err, errAgentClosed), errors.Is(err, errAgentNotActive):
		c.JSON(http.StatusConflict, gin.H{"error": "主机当前不在线", "code": "host_offline"})
	case errors.Is(err, c.Request.Context().Err()):
		// 浏览器已取消请求，没有人在等响应。
		c.Status(http.StatusRequestTimeout)
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "主机返回了无法识别的记录数据", "code": "agent_refused"})
	}
}

// handleRecordInfo 返回一个会话的记录索引：数据库里的索引行 + 在线时来自主机的实时描述（片段列表、完整性等）。
// 主机离线时只返回数据库索引，界面据此提示「主机离线，暂时无法查看内容」。
func (h *Hub) handleRecordInfo(c *gin.Context) {
	code, sid, ok := hostSessionParams(c)
	if !ok {
		return
	}
	row, err := h.store.GetTerminalSession(c.Request.Context(), code, sid)
	if err != nil {
		h.fail(c, err)
		return
	}
	a := h.onlineAgent(code)
	if a == nil || !a.isEnabled() {
		if row == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在", "code": "session_not_found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"online": false, "index": row})
		return
	}
	raw, err := a.statRecord(c.Request.Context(), sid)
	if err != nil {
		h.recordError(c, code, sid, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"online": true, "index": row, "info": raw})
}

// handleRecordData 读取记录内容的一段（二进制事件流，原样转发）。
//
// 查询参数 seg / off 是续读游标（缺省 0 表示从最早保留处开始），max 是本次最多读取的字节数。
// 响应头携带下一段的游标与结束标志；purpose=download 仅用于审计区分「查看」与「下载」。
func (h *Hub) handleRecordData(c *gin.Context) {
	code, sid, ok := hostSessionParams(c)
	if !ok {
		return
	}
	seg, ok1 := parseUint32(c, "seg")
	off, ok2 := parseUint32(c, "off")
	if !ok1 || !ok2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "游标参数不正确", "code": "bad_cursor"})
		return
	}
	max, _ := strconv.Atoi(c.DefaultQuery("max", "0"))
	a := h.agentFor(c, code)
	if a == nil {
		return
	}
	page, err := a.readRecord(c.Request.Context(), sid, seg, off, max)
	if err != nil {
		h.recordError(c, code, sid, err)
		return
	}
	// 审计「谁看了哪个会话」：同一个人对同一个会话的连续读取（分页续读、跟随最新的轮询）合并成一条，
	// 间隔超过 recordAuditWindow 之后的下一次读取再记一条。不依赖游标是否从头开始，
	// 所以从分段中间起读的浏览器视图（长会话只载入最近一段）也不会漏记。
	ev := EvRecordRead
	if c.Query("purpose") == "download" {
		ev = EvRecordDownload
	}
	actor := subjectOf(c)
	if h.recAudit.first(ev+"|"+actor+"|"+code+"|"+sid, h.now()) {
		h.audit.record(c.Request.Context(), auditEvent{Event: ev, Actor: actor, IP: c.ClientIP(), MachineCode: code, SessionID: sid})
	}
	hd := c.Writer.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Rec-Seg", strconv.FormatUint(uint64(page.Seg), 10))
	hd.Set("X-Rec-Off", strconv.FormatUint(uint64(page.Off), 10))
	hd.Set("X-Rec-Next-Seg", strconv.FormatUint(uint64(page.NextSeg), 10))
	hd.Set("X-Rec-Next-Off", strconv.FormatUint(uint64(page.NextOff), 10))
	hd.Set("X-Rec-Eof", strconv.FormatBool(page.EOF))
	hd.Set("X-Rec-Skipped", strconv.FormatBool(page.Skipped))
	hd.Set("Access-Control-Expose-Headers", "X-Rec-Seg, X-Rec-Off, X-Rec-Next-Seg, X-Rec-Next-Off, X-Rec-Eof, X-Rec-Skipped")
	c.Data(http.StatusOK, "application/octet-stream", page.Data)
}

type recordingPolicyRequest struct {
	Off bool `json:"off"`
}

// handleRecordingPolicy 修改一台主机的记录开关（服务端策略：只能让主机更严格，主机本机的 --no-record 永远优先）。
func (h *Hub) handleRecordingPolicy(c *gin.Context) {
	code, ok := machineCodeParam(c)
	if !ok {
		return
	}
	var req recordingPolicyRequest
	if !bindJSON(c, &req) {
		return
	}
	changed, err := h.store.SetTerminalHostRecOff(c.Request.Context(), code, req.Off)
	if err != nil {
		if errors.Is(err, store.ErrTerminalHostNotFound) {
			err = ErrHostNotFound
		}
		h.fail(c, err)
		return
	}
	if changed {
		detail := "off=false"
		if req.Off {
			detail = "off=true"
		}
		h.audit.record(c.Request.Context(), auditEvent{Event: EvRecordPolicy, Actor: subjectOf(c), IP: c.ClientIP(), MachineCode: code, Detail: detail})
		if a := h.onlineAgent(code); a != nil && a.isEnabled() {
			_ = a.writeJSON(termproto.PolicyMsg{T: termproto.TPolicy, Policy: h.policyFor(c.Request.Context(), code)})
		}
	}
	view, err := h.hostView(c.Request.Context(), code)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"host": view})
}

func hostSessionParams(c *gin.Context) (code, sid string, ok bool) {
	code, ok = machineCodeParam(c)
	if !ok {
		return "", "", false
	}
	sid = c.Param("sid")
	if !termproto.ValidSessionID(sid) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "会话 ID 格式不正确", "code": "bad_session_id"})
		return "", "", false
	}
	return code, sid, true
}

func parseUint32(c *gin.Context, name string) (uint32, bool) {
	raw := c.Query(name)
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}
