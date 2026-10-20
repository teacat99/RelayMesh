//go:build e2e && !windows

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 记录相关的 REST 辅助 ----------

type historyEntry struct {
	SID         string `json:"sid"`
	Name        string `json:"name"`
	EndedReason string `json:"ended_reason"`
	ExitCode    *int   `json:"exit_code"`
	Rec         string `json:"rec"`
	RecPurged   bool   `json:"rec_purged"`
}

func (a *api) history(code string) (online bool, list []historyEntry) {
	a.t.Helper()
	var r struct {
		Online   bool           `json:"online"`
		Sessions []historyEntry `json:"sessions"`
	}
	if c := a.raw("GET", "/api/v1/terminal/hosts/"+code+"/history", nil, &r, false); c != 200 {
		a.t.Fatalf("读取历史失败: HTTP %d", c)
	}
	return r.Online, r.Sessions
}

// recordData 经 Hub 把一个会话的记录分页读完（与浏览器回放时的读取方式一致），返回原始事件字节。
func (a *api) recordData(code, sid string) ([]byte, int) {
	a.t.Helper()
	seg, off := "0", "0"
	var all []byte
	for i := 0; i < 256; i++ {
		url := a.base + "/api/v1/terminal/hosts/" + code + "/sessions/" + sid + "/record/data?seg=" + seg + "&off=" + off + "&max=65536"
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+a.jwt)
		req.Header.Set("X-Terminal-Unlock", a.unlock)
		resp, err := a.hc.Do(req)
		if err != nil {
			a.t.Fatalf("读取记录失败: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, resp.StatusCode
		}
		all = append(all, body...)
		if resp.Header.Get("X-Rec-Eof") == "true" {
			return all, 200
		}
		seg, off = resp.Header.Get("X-Rec-Next-Seg"), resp.Header.Get("X-Rec-Next-Off")
	}
	a.t.Fatal("记录分页没有收敛")
	return nil, 0
}

type recordView struct {
	output string
	marks  []record.Mark
}

// decodeRecord 把事件字节解成「终端显示的文本」与标记。
func decodeRecord(t *testing.T, data []byte) recordView {
	t.Helper()
	events, _, err := record.DecodeEvents(data)
	if err != nil {
		t.Fatalf("记录事件解码失败: %v", err)
	}
	var v recordView
	var out bytes.Buffer
	for _, ev := range events {
		switch ev.Kind {
		case record.KindOutput:
			out.Write(ev.Payload)
		case record.KindMark:
			if m, ok := record.DecodeMark(ev.Payload); ok {
				v.marks = append(v.marks, m)
			}
		}
	}
	v.output = out.String()
	return v
}

func (v recordView) endMark() (record.Mark, bool) {
	for _, m := range v.marks {
		if m.T == record.MarkEnd {
			return m, true
		}
	}
	return record.Mark{}, false
}

// waitRecord 等待记录里出现某段文本（写盘有 ≤250ms 的刷新间隔）。
func waitRecord(t *testing.T, h *hub, code, sid, want string, timeout time.Duration) recordView {
	t.Helper()
	var view recordView
	waitUntil(t, timeout, "记录里出现 "+want, func() bool {
		data, status := h.api.recordData(code, sid)
		if status != 200 {
			return false
		}
		view = decodeRecord(t, data)
		return strings.Contains(view.output, want)
	})
	return view
}

func sessionDir(d hostDirs, sid string) string {
	return filepath.Join(d.data, "records", "sessions", sid)
}

// hubStoreContains 在 Hub 的数据库文件（含 WAL）里查找字节串：终端内容永远不应出现在那里。
func hubStoreContains(t *testing.T, h *hub, needle string) bool {
	t.Helper()
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "relaymesh.db") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(h.dir, e.Name()))
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte(needle)) {
			return true
		}
	}
	return false
}

func startRecordingHost(t *testing.T) (*hub, hostDirs, string, string) {
	t.Helper()
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin)
	d := newHostDirs(t)
	startHolder(t, agentBin, d)
	startAgent(t, agentBin, h, d, "agent", "--holder", "external")
	code := h.api.enrollOnly()
	waitHost(t, h, code, "主机上线且守护进程就绪", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	return h, d, code, agentBin
}

// ---------- 用例 ----------

// 记录 = 终端上显示什么就记什么：键入的命令经 Shell 回显出现在记录里，命令输出也在；
// 会话结束后历史里能找到它，原因与退出码正确；Hub 数据库里没有任何终端内容（AC-P8）。
func TestRealRecordCapturesWhatTheTerminalShows(t *testing.T) {
	h, d, code, _ := startRecordingHost(t)

	sess := h.api.openSession(code, "e2e-record")
	if sess.Rec != termproto.RecOn {
		t.Fatalf("新会话应正在录制: %+v", sess)
	}
	tc := h.api.attach(h, code, sess.SID, 0)
	marker := "REC" + randHex(5)
	tc.send("echo " + marker + "-out; exit 3\r")
	tc.waitFor(marker+"-out", 10*time.Second)

	waitUntil(t, 15*time.Second, "历史里出现已结束的会话", func() bool {
		_, list := h.api.history(code)
		return len(list) == 1 && list[0].SID == sess.SID && list[0].EndedReason == "exit"
	})
	_, list := h.api.history(code)
	if list[0].ExitCode == nil || *list[0].ExitCode != 3 || list[0].Name != "e2e-record" || list[0].Rec != termproto.RecOn {
		t.Fatalf("历史条目不符: %+v", list[0])
	}

	view := waitRecord(t, h, code, sess.SID, marker+"-out", 10*time.Second)
	// 键入的命令行出现在记录里（Shell 的回显），命令输出也出现。
	if strings.Count(view.output, marker) < 2 {
		t.Fatalf("记录应同时含键入回显与命令输出（至少两处 %s）:\n%q", marker, view.output)
	}
	var end *record.Mark
	waitUntil(t, 10*time.Second, "记录以 end 标记收尾", func() bool {
		data, _ := h.api.recordData(code, sess.SID)
		v := decodeRecord(t, data)
		if m, ok := v.endMark(); ok {
			end = &m
			return true
		}
		return false
	})
	if end.Reason != "exit" || end.Code == nil || *end.Code != 3 {
		t.Fatalf("end 标记应记录退出原因与退出码: %+v", end)
	}

	// 主机本地确有记录文件，且权限收紧。
	info, err := os.Stat(sessionDir(d, sess.SID))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("记录目录应存在且为 0700: %v %v", info, err)
	}

	// 索引接口：在线时附带主机端的描述。
	var ri struct {
		Online bool            `json:"online"`
		Info   json.RawMessage `json:"info"`
	}
	if c := h.api.raw("GET", "/api/v1/terminal/hosts/"+code+"/sessions/"+sess.SID+"/record", nil, &ri, true); c != 200 || !ri.Online || len(ri.Info) == 0 {
		t.Fatalf("记录索引: HTTP %d %+v", c, ri)
	}

	// Hub 的数据库（含 WAL）里没有任何终端内容。
	if hubStoreContains(t, h, marker) {
		t.Fatal("终端内容泄露进了 Hub 数据库文件")
	}
}

// AC-P14：不回显的密码输入不会出现在记录里（记录的是终端上显示的内容，而不是按键）。
func TestRealRecordOmitsHiddenPasswordInput(t *testing.T) {
	h, _, code, _ := startRecordingHost(t)
	sess := h.api.openSession(code, "e2e-password")
	tc := h.api.attach(h, code, sess.SID, 0)

	secret := "S3c" + randHex(5) // 本测试自己生成的一次性值
	// 提示符用 printf 的格式化拼出来：回显的命令行里不含连续的「PW?」，只有真正打印出的提示符才含，
	// 这样 waitFor("PW?") 一定发生在 stty -echo 生效之后。
	tc.send("stty -echo; printf 'PW%s' '?'; read -r X; stty echo; echo LEN-${#X}-DONE\r")
	tc.waitFor("PW?", 10*time.Second)
	tc.send(secret + "\r")
	tc.waitFor("LEN-"+strconv.Itoa(len(secret))+"-DONE", 10*time.Second)

	if strings.Contains(tc.output(), secret) {
		t.Fatalf("前提不成立：终端上不应显示被隐藏的密码")
	}
	view := waitRecord(t, h, code, sess.SID, "LEN-"+strconv.Itoa(len(secret))+"-DONE", 10*time.Second)
	if strings.Contains(view.output, secret) {
		t.Fatalf("记录里不应出现不回显的密码输入")
	}
	if !strings.Contains(view.output, "PW?") {
		t.Fatalf("提示符应在记录里: %q", view.output)
	}
	tc.send("exit\r")
}

// 记录读取走 Agent 本地磁盘：Agent 被强杀重启后，仍在运行的会话的记录连续可读，新输出继续追加。
func TestRealRecordSurvivesAgentRestart(t *testing.T) {
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin)
	d := newHostDirs(t)
	startHolder(t, agentBin, d)
	ag1 := startAgent(t, agentBin, h, d, "agent1", "--holder", "external")
	code := h.api.enrollOnly()
	waitHost(t, h, code, "上线", func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" })

	sess := h.api.openSession(code, "e2e-restart")
	tc := h.api.attach(h, code, sess.SID, 0)
	before := "BEFORE" + randHex(4)
	tc.send("echo " + before + "\r")
	tc.waitFor(before, 10*time.Second)
	waitRecord(t, h, code, sess.SID, before, 10*time.Second)
	tc.close()

	ag1.kill9()
	waitHost(t, h, code, "主机离线", func(v terminal.HostView) bool { return !v.Online })

	// 主机离线：历史/索引仍来自数据库，内容读取明确告知离线。
	online, _ := h.api.history(code)
	if online {
		t.Fatal("history 应标注主机离线")
	}
	if _, status := h.api.recordData(code, sess.SID); status != http.StatusConflict {
		t.Fatalf("主机离线时读取内容应 409，得到 %d", status)
	}

	startAgent(t, agentBin, h, d, "agent2", "--holder", "external")
	waitHost(t, h, code, "主机重新上线", func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" })
	tc2 := h.api.attach(h, code, sess.SID, 0)
	after := "AFTER" + randHex(4)
	tc2.send("echo " + after + "\r")
	tc2.waitFor(after, 10*time.Second)
	view := waitRecord(t, h, code, sess.SID, after, 10*time.Second)
	if !strings.Contains(view.output, before) {
		t.Fatalf("重启前的内容应仍在同一份记录里: %q", view.output)
	}
	tc2.send("exit\r")
}

// 守护进程被有意停止：会话随之结束，但它们的记录完好，历史里原因为 holder_stopped，回放可用。
func TestRealRecordKeptForSessionsLostToHolderStop(t *testing.T) {
	h, d, code, agentBin := startRecordingHost(t)
	sess := h.api.openSession(code, "doomed")
	tc := h.api.attach(h, code, sess.SID, 0)
	marker := "DOOMED" + randHex(4)
	tc.send("echo " + marker + "\r")
	tc.waitFor(marker, 10*time.Second)
	tc.close()
	waitRecord(t, h, code, sess.SID, marker, 10*time.Second)

	holderStop(t, agentBin, d)
	waitHost(t, h, code, "守护进程不可用", func(v terminal.HostView) bool { return v.Holder != nil && v.Holder.State == "unavailable" })
	startHolder(t, agentBin, d)
	waitHost(t, h, code, "守护进程恢复并报告丢失", func(v terminal.HostView) bool {
		return v.Holder != nil && v.Holder.State == "ready" && len(v.Holder.Lost) > 0
	})

	waitUntil(t, 15*time.Second, "历史里出现 holder_stopped 的会话", func() bool {
		_, list := h.api.history(code)
		return len(list) == 1 && list[0].SID == sess.SID && list[0].EndedReason == termproto.LostHolderStopped
	})
	view := waitRecord(t, h, code, sess.SID, marker, 10*time.Second)
	m, ok := view.endMark()
	if !ok || m.Reason != termproto.LostHolderStopped {
		t.Fatalf("记录应以 holder_stopped 收尾: %+v ok=%v", m, ok)
	}
}

// 无痕会话：主机上不留任何记录文件，也读不到历史；会话本身正常可用。
func TestRealIncognitoSessionLeavesNoRecord(t *testing.T) {
	h, d, code, _ := startRecordingHost(t)

	var r struct {
		Session termproto.SessionInfo `json:"session"`
	}
	if c := h.api.call("POST", "/hosts/"+code+"/sessions", map[string]any{"name": "private", "rows": 30, "cols": 100, "incognito": true}, &r); c != 200 {
		t.Fatalf("开无痕会话失败: HTTP %d", c)
	}
	sess := r.Session
	if sess.Rec != termproto.RecIncognito {
		t.Fatalf("无痕会话应标注 incognito: %+v", sess)
	}
	tc := h.api.attach(h, code, sess.SID, 0)
	secret := "INC" + randHex(5)
	tc.send("echo " + secret + "; exit\r")
	tc.waitFor(secret, 10*time.Second)

	waitUntil(t, 15*time.Second, "无痕会话也进入历史索引", func() bool {
		_, list := h.api.history(code)
		return len(list) == 1 && list[0].SID == sess.SID && list[0].Rec == termproto.RecIncognito
	})
	if _, err := os.Stat(sessionDir(d, sess.SID)); !os.IsNotExist(err) {
		t.Fatalf("无痕会话不得在主机上留下记录目录: %v", err)
	}
	if _, status := h.api.recordData(code, sess.SID); status != http.StatusNotFound {
		t.Fatalf("无痕会话读取记录应 404，得到 %d", status)
	}
	if hubStoreContains(t, h, secret) {
		t.Fatal("终端内容泄露进了 Hub 数据库文件")
	}
}

// 主机级记录开关：管理员在 Web 上关闭后，新会话不再记录；重新打开后恢复。（策略经 Hub→Agent→守护进程真实下发）
func TestRealHostRecordingSwitch(t *testing.T) {
	h, d, code, _ := startRecordingHost(t)

	if c := h.api.call("POST", "/hosts/"+code+"/recording", map[string]bool{"off": true}, nil); c != 200 {
		t.Fatalf("关闭记录失败: HTTP %d", c)
	}
	waitHost(t, h, code, "主机视图反映 rec_off", func(v terminal.HostView) bool { return v.RecOff })
	var off termproto.SessionInfo
	waitUntil(t, 15*time.Second, "新会话不再记录（策略已下发到守护进程）", func() bool {
		off = h.api.openSession(code, "while-off")
		return off.Rec == termproto.RecOff
	})
	if _, err := os.Stat(sessionDir(d, off.SID)); !os.IsNotExist(err) {
		t.Fatalf("记录关闭时不得留下记录目录: %v", err)
	}

	if c := h.api.call("POST", "/hosts/"+code+"/recording", map[string]bool{"off": false}, nil); c != 200 {
		t.Fatalf("重新开启记录失败: HTTP %d", c)
	}
	waitUntil(t, 15*time.Second, "重新开启后新会话恢复记录", func() bool {
		s := h.api.openSession(code, "while-on")
		return s.Rec == termproto.RecOn
	})
}

// 本机一票否决：--no-record 的主机不记录任何会话，服务端无法覆盖。
func TestRealLocalVetoCannotBeOverridden(t *testing.T) {
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin)
	d := newHostDirs(t)
	startAgent(t, agentBin, h, d, "agent", "--holder", "embedded", "--no-record")
	code := h.api.enrollOnly()
	waitHost(t, h, code, "主机上线", func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" })

	// 服务端明确要求开启记录（rec_off=false 是默认值，这里再显式设置一次）。
	if c := h.api.call("POST", "/hosts/"+code+"/recording", map[string]bool{"off": false}, nil); c != 200 {
		t.Fatalf("设置记录策略失败: HTTP %d", c)
	}
	sess := h.api.openSession(code, "vetoed")
	if sess.Rec != termproto.RecVeto {
		t.Fatalf("--no-record 的主机会话应标注 veto: %+v", sess)
	}
	tc := h.api.attach(h, code, sess.SID, 0)
	m := "VETO" + randHex(4)
	tc.send("echo " + m + "\r")
	tc.waitFor(m, 10*time.Second)
	if _, err := os.Stat(sessionDir(d, sess.SID)); !os.IsNotExist(err) {
		t.Fatalf("一票否决的主机不得留下记录: %v", err)
	}
	if _, status := h.api.recordData(code, sess.SID); status != http.StatusNotFound {
		t.Fatalf("否决后读取记录应 404，得到 %d", status)
	}
	tc.send("exit\r")
}
