package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 辅助 ----------

// dumpAllTables 把数据库里所有表的全部内容拼成一个字符串，用来断言「终端内容从未进过数据库」。
func dumpAllTables(t *testing.T, st *store.Store) string {
	t.Helper()
	db := st.DB()
	rows, err := db.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Rows()
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	_ = rows.Close()

	var sb strings.Builder
	for _, name := range tables {
		r, err := db.Raw("SELECT * FROM `" + name + "`").Rows()
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			sb.WriteString(name)
			for i, v := range vals {
				sb.WriteString("|")
				sb.WriteString(cols[i])
				sb.WriteString("=")
				switch x := v.(type) {
				case []byte:
					sb.Write(x)
				default:
					fmt.Fprint(&sb, x)
				}
			}
			sb.WriteString("\n")
		}
		_ = r.Close()
	}
	return sb.String()
}

func (e *testEnv) auditEvents(event string) []model.TerminalAuditLog {
	e.t.Helper()
	logs, err := e.store.ListTerminalAudit(context.Background(), 500, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []model.TerminalAuditLog
	for _, l := range logs {
		if l.Event == event {
			out = append(out, l)
		}
	}
	return out
}

func (e *testEnv) sessionRow(code, sid string) *model.TerminalSession {
	e.t.Helper()
	row, err := e.store.GetTerminalSession(context.Background(), code, sid)
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

func recordPath(code, sid, tail string) string {
	return "/api/v1/terminal/hosts/" + code + "/sessions/" + sid + "/record" + tail
}

// shortRecordTimeouts 缩短记录读取的等待期限（测试结束后恢复）。包级变量是进程共享的，
// 所以用它的测试会被钉成串行（见 overrideGlobal）：要在这个测试第一次 newTestEnv 之前调用。
func shortRecordTimeouts(t *testing.T, total, idle time.Duration) {
	t.Helper()
	overrideGlobal(t, &recordReadTimeout, total)
	overrideGlobal(t, &recordIdleTimeout, idle)
}

// ---------- 读取记录 ----------

func TestRecordReadStreamsPagesAndAudits(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("rec-host"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "build")

	content := make([]byte, 150<<10)
	for i := range content {
		content[i] = byte(i*7 + i>>8)
	}
	a.setRecord(sid, content, `{"v":1,"state":"on"}`)

	var got []byte
	seg, off := "0", "0"
	pages := 0
	for {
		code, h, body := e.callRaw("GET", recordPath(a.code, sid, "/data?seg="+seg+"&off="+off+"&max=65536"), hdr...)
		if code != http.StatusOK {
			t.Fatalf("读取记录失败: %d %s", code, body)
		}
		if ct := h.Get("Content-Type"); ct != "application/octet-stream" {
			t.Fatalf("Content-Type=%q", ct)
		}
		if h.Get("Cache-Control") != "no-store" {
			t.Fatalf("记录内容不得被缓存: %q", h.Get("Cache-Control"))
		}
		got = append(got, body...)
		pages++
		if h.Get("X-Rec-Eof") == "true" {
			break
		}
		seg, off = h.Get("X-Rec-Next-Seg"), h.Get("X-Rec-Next-Off")
		if pages > 8 {
			t.Fatal("分页没有收敛")
		}
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("分页拼接后的内容应与主机记录一致 (got %d, want %d)", len(got), len(content))
	}
	if pages != 3 {
		t.Fatalf("150KiB 按 64KiB 分页应为 3 页，得到 %d", pages)
	}

	// 审计只记「谁看了哪个会话」，只在从头开始的那一次记录，不含内容；续读不重复记录。
	reads := e.auditEvents(EvRecordRead)
	if len(reads) != 1 || reads[0].SessionID != sid || reads[0].MachineCode != a.code || reads[0].Actor != testUser {
		t.Fatalf("应恰有 1 条查看审计: %+v", reads)
	}
	if reads[0].Detail != "" {
		t.Fatalf("审计详情不得携带内容: %q", reads[0].Detail)
	}

	// 下载用单独的审计事件。
	if code, _, _ := e.callRaw("GET", recordPath(a.code, sid, "/data?purpose=download&max=1024"), hdr...); code != 200 {
		t.Fatalf("下载失败: %d", code)
	}
	if n := len(e.auditEvents(EvRecordDownload)); n != 1 {
		t.Fatalf("应恰有 1 条下载审计: %d", n)
	}
}

func TestRecordAuditGateMergesBurstsPerKey(t *testing.T) {
	var g recordAuditGate
	t0 := time.Unix(1_700_000_000, 0)
	if !g.first("a", t0) {
		t.Fatal("第一次读取应记审计")
	}
	if g.first("a", t0.Add(recordAuditWindow-time.Second)) {
		t.Fatal("窗口内的连续读取不应重复记审计")
	}
	// 上一次读取刷新了时间：窗口是「滑动」的，持续读取的长时间查看始终只记一条。
	if g.first("a", t0.Add(2*recordAuditWindow-2*time.Second)) {
		t.Fatal("持续读取应一直算同一次查看")
	}
	if !g.first("b", t0.Add(time.Second)) {
		t.Fatal("不同的键互不影响")
	}
	if !g.first("a", t0.Add(4*recordAuditWindow)) {
		t.Fatal("停手超过窗口之后的读取应视为新的一次查看")
	}
}

func TestRecordAuditGateStaysBounded(t *testing.T) {
	var g recordAuditGate
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < recordAuditMax*3; i++ {
		g.first(fmt.Sprintf("k%d", i), now)
	}
	g.mu.Lock()
	n := len(g.last)
	g.mu.Unlock()
	if n > recordAuditMax {
		t.Fatalf("去重表应有上限，得到 %d 条", n)
	}
}

func TestRecordReadAuditIsNotBypassedByMidSegmentCursor(t *testing.T) {
	e := newTestEnv(t, withFakeClock(), withUnlockTTL(time.Hour, 24*time.Hour))
	a := e.enrollAgent(testMeta("rec-audit"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "x")
	a.setRecord(sid, bytes.Repeat([]byte("x"), 4096), `{}`)

	read := func(query string) {
		t.Helper()
		if code, _, body := e.callRaw("GET", recordPath(a.code, sid, "/data?"+query), hdr...); code != http.StatusOK {
			t.Fatalf("读取 %s 失败: %d %s", query, code, body)
		}
	}
	count := func(ev string) int { return len(e.auditEvents(ev)) }

	// 浏览器对长会话只载入最近的分段：游标不是从头开始，也必须记录这次查看。
	read("seg=7&off=0&max=1024")
	if n := count(EvRecordRead); n != 1 {
		t.Fatalf("从分段中间起读也应记 1 条查看审计，得到 %d", n)
	}
	// 翻页与跟随轮询（间隔小于窗口）算同一次查看，不重复。
	e.clock.Advance(recordAuditWindow * 3 / 4)
	read("seg=7&off=1024&max=1024")
	e.clock.Advance(recordAuditWindow * 3 / 4)
	read("seg=7&off=2048&max=1024")
	if n := count(EvRecordRead); n != 1 {
		t.Fatalf("连续读取应合并成一条，得到 %d", n)
	}
	// 停手超过窗口后再来，是新的一次查看。
	e.clock.Advance(recordAuditWindow * 2)
	read("seg=7&off=0&max=1024")
	if n := count(EvRecordRead); n != 2 {
		t.Fatalf("隔了一段时间再看应再记 1 条，得到 %d", n)
	}
	// 下载与查看分开记；审计里始终没有内容。
	read("purpose=download&seg=3&off=1024&max=1024")
	if n := count(EvRecordDownload); n != 1 {
		t.Fatalf("应记 1 条下载审计，得到 %d", n)
	}
	for _, ev := range append(e.auditEvents(EvRecordRead), e.auditEvents(EvRecordDownload)...) {
		if ev.Detail != "" || ev.SessionID != sid {
			t.Fatalf("审计只记谁看了哪个会话: %+v", ev)
		}
	}
}

func TestRecordEndpointsRequireHumanLoginAndUnlock(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("rec-host"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "x")
	a.setRecord(sid, []byte("hello"), `{}`)

	loginOnly := []string{"Authorization", humanAuth(testUser)}
	for _, path := range []string{recordPath(a.code, sid, ""), recordPath(a.code, sid, "/data")} {
		if code, _, body := e.callRaw("GET", path, loginOnly...); code != http.StatusForbidden || !strings.Contains(string(body), "unlock_required") {
			t.Fatalf("%s 未解锁应被拒绝: %d %s", path, code, body)
		}
		if code, _, _ := e.callRaw("GET", path); code != http.StatusUnauthorized {
			t.Fatalf("%s 未登录应 401: %d", path, code)
		}
		// MCP 凭据 / 随便一个令牌不是人类身份。
		if code, _, _ := e.callRaw("GET", path, "Authorization", "Bearer mcp-token", unlockHeader, "x"); code != http.StatusUnauthorized {
			t.Fatalf("%s 非人类凭据应 401: %d", path, code)
		}
	}
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/recording", map[string]bool{"off": true}, loginOnly...); res.Status != http.StatusForbidden {
		t.Fatalf("修改记录开关需要解锁: %d %s", res.Status, res.Raw)
	}
	// 历史索引只需登录。
	if res := e.call("GET", "/api/v1/terminal/hosts/"+a.code+"/history", nil, loginOnly...); res.Status != 200 {
		t.Fatalf("历史索引只需登录: %d %s", res.Status, res.Raw)
	}
}

func TestRecordInfoOnlineAndOffline(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("rec-host"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "build")
	a.setRecord(sid, []byte("x"), `{"v":1,"state":"on","raw_bytes":1}`)
	eventually(t, 2*time.Second, "索引行出现", func() bool { return e.sessionRow(a.code, sid) != nil })

	res := e.call("GET", recordPath(a.code, sid, ""), nil, hdr...)
	if res.Status != 200 || res.Body["online"] != true {
		t.Fatalf("在线应返回实时描述: %d %s", res.Status, res.Raw)
	}
	info, _ := res.Body["info"].(map[string]any)
	if info["state"] != "on" {
		t.Fatalf("应透传主机端的记录描述: %s", res.Raw)
	}
	if idx, _ := res.Body["index"].(map[string]any); idx["sid"] != sid {
		t.Fatalf("应附带数据库索引: %s", res.Raw)
	}

	// 主机离线：只返回数据库里的索引，读取内容明确告知离线。
	a.close()
	eventually(t, 2*time.Second, "离线", func() bool { return e.hub.onlineAgent(a.code) == nil })
	res = e.call("GET", recordPath(a.code, sid, ""), nil, hdr...)
	if res.Status != 200 || res.Body["online"] != false || res.Body["info"] != nil {
		t.Fatalf("离线应只返回索引: %d %s", res.Status, res.Raw)
	}
	if code, _, body := e.callRaw("GET", recordPath(a.code, sid, "/data"), hdr...); code != http.StatusConflict || !strings.Contains(string(body), "host_offline") {
		t.Fatalf("离线读取内容应 409 host_offline: %d %s", code, body)
	}
	// 完全不认识的会话 + 离线 → 404。
	if res := e.call("GET", recordPath(a.code, strings.Repeat("e", 32), ""), nil, hdr...); res.Status != http.StatusNotFound {
		t.Fatalf("未知会话离线应 404: %d %s", res.Status, res.Raw)
	}
	// 非法 SID。
	if res := e.call("GET", recordPath(a.code, "../../etc", ""), nil, hdr...); res.Status == 200 {
		t.Fatalf("非法 SID 不得成功: %s", res.Raw)
	}
}

func TestRecordErrorMapping(t *testing.T) {
	shortRecordTimeouts(t, 600*time.Millisecond, 300*time.Millisecond)
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("rec-host"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "x")
	eventually(t, 2*time.Second, "索引行出现", func() bool { return e.sessionRow(a.code, sid) != nil })

	// 主机上没有这个会话的记录：404，并在索引里标记已清理（不再出现在历史里）。
	code, _, body := e.callRaw("GET", recordPath(a.code, sid, "/data"), hdr...)
	if code != http.StatusNotFound || !strings.Contains(string(body), "record_gone") {
		t.Fatalf("无记录应 404 record_gone: %d %s", code, body)
	}
	if row := e.sessionRow(a.code, sid); row == nil || !row.RecPurged {
		t.Fatalf("应标记记录已被清理: %+v", row)
	}

	// 主机繁忙。
	a.mu.Lock()
	a.recFail = termproto.RecReasonBusy
	a.mu.Unlock()
	if code, _, body := e.callRaw("GET", recordPath(a.code, sid, "/data"), hdr...); code != http.StatusTooManyRequests || !strings.Contains(string(body), "busy") {
		t.Fatalf("繁忙应 429: %d %s", code, body)
	}

	// 主机不应答：超时 504，且不留下悬挂的等待登记。
	a.mu.Lock()
	a.recFail, a.recSilent = "", true
	a.mu.Unlock()
	if code, _, body := e.callRaw("GET", recordPath(a.code, sid, "/data"), hdr...); code != http.StatusGatewayTimeout || !strings.Contains(string(body), "agent_timeout") {
		t.Fatalf("不应答应 504: %d %s", code, body)
	}
	if res := e.call("GET", recordPath(a.code, sid, ""), nil, hdr...); res.Status != http.StatusGatewayTimeout {
		t.Fatalf("索引读取不应答也应 504: %d %s", res.Status, res.Raw)
	}
	ag := e.hub.onlineAgent(a.code)
	ag.mu.Lock()
	pending := len(ag.recWaits)
	ag.mu.Unlock()
	if pending != 0 {
		t.Fatalf("超时后不应残留等待登记: %d", pending)
	}
}

func TestRecordRequestsInFlightAreCapped(t *testing.T) {
	shortRecordTimeouts(t, 900*time.Millisecond, 900*time.Millisecond)
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("rec-host"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "x")
	a.setRecord(sid, []byte("x"), `{}`)
	a.mu.Lock()
	a.recSilent = true // 请求一直挂着
	a.mu.Unlock()

	var wg sync.WaitGroup
	statuses := make(chan int, maxPendingRecordReqs+3)
	for i := 0; i < maxPendingRecordReqs+3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, _ := e.callRaw("GET", recordPath(a.code, sid, "/data"), hdr...)
			statuses <- code
		}()
	}
	wg.Wait()
	close(statuses)
	busy, timeout := 0, 0
	for code := range statuses {
		switch code {
		case http.StatusTooManyRequests:
			busy++
		case http.StatusGatewayTimeout:
			timeout++
		default:
			t.Fatalf("意外状态码 %d", code)
		}
	}
	if timeout != maxPendingRecordReqs || busy != 3 {
		t.Fatalf("同时在途的记录请求应被限制在 %d 个: timeout=%d busy=%d", maxPendingRecordReqs, timeout, busy)
	}
}

func TestRecordFrameForUnknownRequestIsIgnored(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("rec-host"), "")
	// 没有任何在途请求时，Agent 乱发的记录帧 / 应答既不能崩溃也不能断开连接。
	a.sendBinary(termproto.EncodeRecord(termproto.RecordChunk{Req: 77, Flags: termproto.RecFlagLast, Data: []byte("zzz")}))
	a.send(termproto.RecordInfo{T: termproto.TRecordInfo, Req: 78, SID: strings.Repeat("a", 32), Info: json.RawMessage(`{}`)})
	a.send(termproto.RecordFail{T: termproto.TRecordFail, Req: 79, Reason: "x"})
	time.Sleep(150 * time.Millisecond)
	if e.hub.onlineAgent(a.code) == nil {
		t.Fatal("无主的记录帧不应导致连接被断开")
	}
	// 畸形的记录帧（过短）是协议违例：断开。
	a.sendBinary([]byte{termproto.FrameRecord, 0, 0})
	if code := a.waitClosed(2 * time.Second); code != termproto.CloseInvalidHandshake {
		t.Fatalf("畸形记录帧应以协议违例关闭，得到 %d", code)
	}
}

// ---------- 会话索引与历史 ----------

func TestSessionIndexLifecycleAndHistory(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("idx-host"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "deploy")

	eventually(t, 2*time.Second, "运行中的会话进入索引", func() bool {
		r := e.sessionRow(a.code, sid)
		return r != nil && r.Name == "deploy" && r.EndedAt == nil && r.RecState == termproto.RecOn && r.StartedAt > 0
	})
	// 运行中的会话不在「历史」里。
	res := e.call("GET", "/api/v1/terminal/hosts/"+a.code+"/history", nil, hdr...)
	if list, _ := res.Body["sessions"].([]any); len(list) != 0 || res.Body["online"] != true {
		t.Fatalf("运行中的会话不应出现在历史里: %s", res.Raw)
	}

	// 终止会话 → 已结束，原因 killed（比 Agent 报告的 exit 更精确），并出现在历史里。
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions/"+sid+"/kill", nil, hdr...); res.Status != http.StatusAccepted {
		t.Fatalf("kill: %d %s", res.Status, res.Raw)
	}
	eventually(t, 3*time.Second, "会话记为 killed", func() bool {
		r := e.sessionRow(a.code, sid)
		return r != nil && r.EndedAt != nil && r.EndedReason == "killed" && r.ExitCode != nil && *r.ExitCode == 137
	})
	res = e.call("GET", "/api/v1/terminal/hosts/"+a.code+"/history", nil, hdr...)
	list, _ := res.Body["sessions"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["sid"] != sid || list[0].(map[string]any)["ended_reason"] != "killed" {
		t.Fatalf("已结束的会话应出现在历史里: %s", res.Raw)
	}

	// 主机离线后历史仍可查看（来自数据库）。
	a.close()
	eventually(t, 2*time.Second, "离线", func() bool { return e.hub.onlineAgent(a.code) == nil })
	res = e.call("GET", "/api/v1/terminal/hosts/"+a.code+"/history", nil, hdr...)
	if list, _ := res.Body["sessions"].([]any); len(list) != 1 || res.Body["online"] != false {
		t.Fatalf("离线时历史仍应可用: %s", res.Raw)
	}

	// 删除主机会一并清掉它的会话索引。
	if res := e.call("DELETE", "/api/v1/terminal/hosts/"+a.code, nil, hdr...); res.Status != 200 {
		t.Fatalf("删除主机: %d %s", res.Status, res.Raw)
	}
	if rows, _ := e.store.ListTerminalSessions(context.Background(), a.code, false, 0); len(rows) != 0 {
		t.Fatalf("删除主机后不应残留会话索引: %+v", rows)
	}
}

func TestClearingAnExitedSessionKeepsItsEndReason(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("idx-clear"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "done")
	eventually(t, 2*time.Second, "会话进入索引", func() bool { return e.sessionRow(a.code, sid) != nil })

	code := 0
	a.mu.Lock()
	a.sessions[sid].info.State = termproto.SessionExited
	a.sessions[sid].info.ExitCode = &code
	a.mu.Unlock()
	a.send(termproto.Exit{T: termproto.TExit, SID: sid, Code: &code})
	a.sendSessions()
	eventually(t, 3*time.Second, "会话记为 exit", func() bool {
		r := e.sessionRow(a.code, sid)
		return r != nil && r.EndedAt != nil && r.EndedReason == "exit"
	})
	eventually(t, 2*time.Second, "Hub 看到的会话已是 exited", func() bool {
		ag := e.hub.onlineAgent(a.code)
		if ag == nil {
			return false
		}
		info, ok := ag.findSession(sid)
		return ok && info.State == termproto.SessionExited
	})

	// 把已退出的会话从列表里清掉，不是「被终止」：结束原因保持 exit。
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions/"+sid+"/kill", nil, hdr...); res.Status != http.StatusAccepted {
		t.Fatalf("kill: %d %s", res.Status, res.Raw)
	}
	time.Sleep(400 * time.Millisecond) // 索引的结束事件是即时处理的；若被改写，这点时间足够看到
	if r := e.sessionRow(a.code, sid); r == nil || r.EndedReason != "exit" {
		t.Fatalf("清理已退出的会话不应把结束原因改成 killed: %+v", r)
	}
}

func TestSessionExitIsIndexedWithExitCode(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("idx-host"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "job")
	eventually(t, 2*time.Second, "会话进入索引", func() bool { return e.sessionRow(a.code, sid) != nil })

	code := 7
	a.mu.Lock()
	s := a.sessions[sid]
	s.info.State = termproto.SessionExited
	s.info.ExitCode = &code
	a.mu.Unlock()
	a.send(termproto.Exit{T: termproto.TExit, SID: sid, Code: &code})

	eventually(t, 3*time.Second, "会话记为 exit(7)", func() bool {
		r := e.sessionRow(a.code, sid)
		return r != nil && r.EndedAt != nil && r.EndedReason == "exit" && r.ExitCode != nil && *r.ExitCode == 7
	})
}

func TestLostSessionsAreIndexedWithPreciseReason(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("idx-host"), "")
	hdr := e.admin()
	known := e.openSession(a, hdr, "known")
	eventually(t, 2*time.Second, "已知会话进入索引", func() bool { return e.sessionRow(a.code, known) != nil })

	unknown := strings.Repeat("d", 32)
	at := time.Now().Add(-time.Hour).Unix()
	a.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Lost: []termproto.LostSession{
		{SID: known, Reason: termproto.LostHostReboot, At: at},
		{SID: unknown, Name: "before-reboot", Created: at - 600, Reason: termproto.LostHostReboot, At: at},
	}})

	eventually(t, 3*time.Second, "丢失的会话记为 host_reboot", func() bool {
		k, u := e.sessionRow(a.code, known), e.sessionRow(a.code, unknown)
		return k != nil && k.EndedReason == "host_reboot" && u != nil && u.EndedReason == "host_reboot" &&
			u.Name == "before-reboot" && u.StartedAt == at-600 && u.EndedAt != nil && u.EndedAt.Unix() == at
	})
	// 它们的记录仍在主机磁盘上，历史里应能找到入口。
	res := e.call("GET", "/api/v1/terminal/hosts/"+a.code+"/history", nil, hdr...)
	if list, _ := res.Body["sessions"].([]any); len(list) != 2 {
		t.Fatalf("历史应包含两个丢失的会话: %s", res.Raw)
	}
}

func TestTemporaryHostSessionsAreNotIndexed(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	hdr := e.admin()
	e.openDiscovery(hdr)
	meta := testMeta("win-temp")
	meta.Kind = termproto.KindTemporary
	_, priv := newFakeIdentity(t)
	tmp, _, _ := e.dialAgent(priv, meta, nil)
	if err := tmp.handshake(meta); err != nil {
		t.Fatal(err)
	}
	tmp.start()
	t.Cleanup(tmp.close)
	eventually(t, 2*time.Second, "待启用", func() bool { return len(e.hub.pending.snapshot()) == 1 })
	if res := e.call("POST", "/api/v1/terminal/hosts/"+tmp.code+"/enable", nil, hdr...); res.Status != 200 {
		t.Fatalf("启用: %d %s", res.Status, res.Raw)
	}
	select {
	case <-tmp.approved:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到 approved")
	}
	tmpSID := e.openSession(tmp, hdr, "temp-shell")

	// 对照：持久主机的会话会进入索引。因为索引协程按序处理，对照行出现时临时主机的任务早已处理完。
	perm := e.enrollAgent(testMeta("perm"), "")
	permSID := e.openSession(perm, hdr, "perm-shell")
	eventually(t, 2*time.Second, "持久主机会话进入索引", func() bool { return e.sessionRow(perm.code, permSID) != nil })

	if rows, _ := e.store.ListTerminalSessions(context.Background(), tmp.code, false, 0); len(rows) != 0 {
		t.Fatalf("临时启用的主机不得留下会话索引: %+v (sid=%s)", rows, tmpSID)
	}
}

// AC-P8：终端内容（输出、输入、记录）永远不进数据库和审计日志。
func TestTerminalContentNeverReachesDatabase(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("scan-host"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "scan")

	const secret = "ZX-SECRET-MARKER-9f3b"
	a.setRecord(sid, []byte("recorded output "+secret), `{"v":1,"note":"`+secret+`"}`)
	a.emit(sid, []byte("live output "+secret+"\r\n"))

	// 经 WebSocket 真实地输入一次，让输入也走过 Hub。
	ws, _, err := e.dialBrowser(e.ticket(a, sid, hdr, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	readReady(t, ws)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ws.Write(ctx, 2 /* binary */, termproto.EncodeBrowserInput([]byte("typed "+secret+"\n"))); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "输入到达 Agent", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, in := range a.inputs {
			if bytes.Contains(in, []byte(secret)) {
				return true
			}
		}
		return false
	})

	// 读记录、读索引、读历史、改名（含标签）、终止，覆盖全部会触碰数据库的路径。
	if code, _, _ := e.callRaw("GET", recordPath(a.code, sid, "/data"), hdr...); code != 200 {
		t.Fatalf("读取记录: %d", code)
	}
	e.call("GET", recordPath(a.code, sid, ""), nil, hdr...)
	e.call("GET", "/api/v1/terminal/hosts/"+a.code+"/history", nil, hdr...)
	e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions/"+sid+"/kill", nil, hdr...)
	eventually(t, 3*time.Second, "会话结束进入索引", func() bool {
		r := e.sessionRow(a.code, sid)
		return r != nil && r.EndedAt != nil
	})

	if dump := dumpAllTables(t, e.store); strings.Contains(dump, secret) {
		t.Fatalf("终端内容泄露进了数据库:\n%s", dump)
	}
}

// ---------- 无痕会话与记录策略 ----------

func TestIncognitoFlagReachesAgentAndIsAudited(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("inc-host"), "")
	hdr := e.admin()

	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions", map[string]any{"name": "secret work", "rows": 24, "cols": 80, "incognito": true}, hdr...)
	if res.Status != 200 {
		t.Fatalf("新建无痕会话: %d %s", res.Status, res.Raw)
	}
	sess, _ := res.Body["session"].(map[string]any)
	if sess["rec"] != termproto.RecIncognito {
		t.Fatalf("无痕会话的录制状态应为 incognito: %s", res.Raw)
	}
	normal := e.openSession(a, hdr, "normal")

	a.mu.Lock()
	opens := append([]termproto.Open(nil), a.opens...)
	a.mu.Unlock()
	if len(opens) != 2 || !opens[0].Incognito || opens[1].Incognito {
		t.Fatalf("incognito 应原样转给 Agent，且默认关闭: %+v", opens)
	}
	audits := e.auditEvents(EvSessionOpen)
	incognito := 0
	for _, l := range audits {
		if l.Detail == "incognito" {
			incognito++
		}
	}
	if incognito != 1 || len(audits) != 2 {
		t.Fatalf("应有 2 条打开审计，其中 1 条标注 incognito: %+v", audits)
	}
	eventually(t, 2*time.Second, "索引同步录制状态", func() bool {
		r := e.sessionRow(a.code, normal)
		return r != nil && r.RecState == termproto.RecOn
	})
}

func lastPolicy(a *fakeAgent) (termproto.Policy, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.policies) == 0 {
		return termproto.Policy{}, false
	}
	return a.policies[len(a.policies)-1], true
}

func TestHostRecordingSwitchPushesPolicyAndSurvivesReconnect(t *testing.T) {
	e := newTestEnv(t)
	meta := testMeta("pol-host")
	a := e.enrollAgent(meta, "")
	hdr := e.admin()

	// 初始：记录开启（批准时下发的策略不关闭记录）。
	if a.welcome.Policy.RecOff {
		t.Fatal("默认不应关闭记录")
	}

	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/recording", map[string]bool{"off": true}, hdr...)
	if res.Status != 200 {
		t.Fatalf("关闭记录: %d %s", res.Status, res.Raw)
	}
	host, _ := res.Body["host"].(map[string]any)
	if host["rec_off"] != true {
		t.Fatalf("主机视图应反映 rec_off: %s", res.Raw)
	}
	eventually(t, 2*time.Second, "Agent 收到关闭记录的策略", func() bool {
		p, ok := lastPolicy(a)
		return ok && p.RecOff && p.Terminal
	})
	if n := len(e.auditEvents(EvRecordPolicy)); n != 1 {
		t.Fatalf("应有 1 条策略审计: %d", n)
	}
	// 重复设置相同的值不产生审计也不重复下发。
	e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/recording", map[string]bool{"off": true}, hdr...)
	if n := len(e.auditEvents(EvRecordPolicy)); n != 1 {
		t.Fatalf("重复设置不应再次审计: %d", n)
	}

	// 重连后的 welcome 仍带关闭策略（策略保存在 Hub，不依赖 Agent 记忆）。
	a.close()
	eventually(t, 2*time.Second, "离线", func() bool { return e.hub.onlineAgent(a.code) == nil })
	b := e.connectEnabled(a.priv, meta)
	if !b.welcome.Policy.RecOff {
		t.Fatalf("重连的 welcome 应带 rec_off: %+v", b.welcome.Policy)
	}
	hosts := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	if list, _ := hosts.Body["hosts"].([]any); len(list) != 1 || list[0].(map[string]any)["rec_off"] != true {
		t.Fatalf("主机列表应带 rec_off: %s", hosts.Raw)
	}

	// 再打开。
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/recording", map[string]bool{"off": false}, hdr...); res.Status != 200 {
		t.Fatalf("重新开启: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "Agent 收到重新开启的策略", func() bool {
		p, ok := lastPolicy(b)
		return ok && !p.RecOff
	})

	// 不存在的主机。
	unknownPub, _ := newFakeIdentity(t)
	if res := e.call("POST", "/api/v1/terminal/hosts/"+termproto.MachineCode(unknownPub)+"/recording", map[string]bool{"off": true}, hdr...); res.Status != http.StatusNotFound {
		t.Fatalf("未知主机应 404: %d %s", res.Status, res.Raw)
	}
}

func approvedPolicy(a *fakeAgent) termproto.Policy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.approvedPol
}

func TestServerWidePolicyIsPushedToEveryHost(t *testing.T) {
	e := newTestEnv(t, func(o *Options, _ *testEnv) {
		o.RecordRetentionDays = 7
		o.RecordSessionMB = 32
		o.RecordHostMB = 256
	})
	meta := testMeta("glob-host")
	a := e.enrollAgent(meta, "")
	// 批准那一刻随 approved 下发。
	if p := approvedPolicy(a); p.RecRetentionDays != 7 || p.RecSessionMB != 32 || p.RecHostMB != 256 || p.RecOff {
		t.Fatalf("approved 应带服务端记录策略: %+v", p)
	}
	// 之后重连时随 welcome 下发。
	a.close()
	eventually(t, 2*time.Second, "离线", func() bool { return e.hub.onlineAgent(a.code) == nil })
	b := e.connectEnabled(a.priv, meta)
	if p := b.welcome.Policy; p.RecRetentionDays != 7 || p.RecSessionMB != 32 || p.RecHostMB != 256 || p.RecOff {
		t.Fatalf("welcome 应带服务端记录策略: %+v", p)
	}

	// 全局关闭：每台主机都收到 rec_off，且主机视图标注 rec_server_off。
	off := newTestEnv(t, func(o *Options, _ *testEnv) { o.RecordOff = true })
	offMeta := testMeta("off-host")
	c := off.enrollAgent(offMeta, "")
	if !approvedPolicy(c).RecOff {
		t.Fatalf("全局关闭应随 approved 下发: %+v", approvedPolicy(c))
	}
	hosts := off.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	if list, _ := hosts.Body["hosts"].([]any); len(list) != 1 || list[0].(map[string]any)["rec_server_off"] != true {
		t.Fatalf("全局关闭应在主机视图里标注: %s", hosts.Raw)
	}
	c.close()
	eventually(t, 2*time.Second, "离线", func() bool { return off.hub.onlineAgent(c.code) == nil })
	d := off.connectEnabled(c.priv, offMeta)
	if !d.welcome.Policy.RecOff {
		t.Fatalf("全局关闭应随 welcome 下发 rec_off: %+v", d.welcome.Policy)
	}
}

func TestResolveRecord(t *testing.T) {
	for raw, wantOff := range map[string]bool{"": false, "auto": false, "ON": false, "off": true, "false": true, "0": true} {
		if off, w := ResolveRecord(raw); off != wantOff || w != "" {
			t.Errorf("ResolveRecord(%q) = %v, %q", raw, off, w)
		}
	}
	if off, w := ResolveRecord("maybe"); !off || w == "" {
		t.Errorf("无法识别的取值应按关闭处理并给出提示: %v %q", off, w)
	}
}
