package store

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

// Agent 身份与状态的存储层（#113，设计稿 04 §2.5）。
//
// 纯判断都在 agentstate 包里；这里负责三件事：
//  1. 认出「谁」：把一次汇报映射到 agent_identities（Token + 主机 + 项目路径），项目路径取调用方自报的，
//     没有就用工作流上记录的绑定（最近一次汇报会话的目录）；
//  2. 把事件折进会话快照（内存里是权威，库里是持久化），状态变了或心跳到期才写库，思考心跳与重复事件不写；
//  3. 给 API 提供列表、改名与展示状态（Evaluate）。
//
// 与旧的 AgentLink / ComputeAgentLive / 会话守护并存、互不依赖：旧链路照旧工作，新状态核心在旁边跑。

const (
	agentIdentityWriteEvery    = 20 * time.Second // 身份的 last_seen / last_mcp_at 等写库节流
	agentSessionHeartbeatEvery = 30 * time.Second // 会话没有结构性变化时，最久多久写一次心跳
	agentEventsKeepFor         = 7 * 24 * time.Hour
	agentEventsKeepRows        = 20000
	agentSessionsKeepFor       = 90 * 24 * time.Hour
	agentPruneEveryIngests     = 500
	agentPruneEvery            = 6 * time.Hour
	agentBindingTTL            = 10 * time.Minute
	agentBindingMissTTL        = time.Minute
	agentMemMax                = 2000 // 内存里的身份 / 会话 / 绑定条目上限，超过就清陈旧的

	// AgentNameMaxRunes 是身份改名的最大长度。
	AgentNameMaxRunes = 80
)

type agentIdentityKey struct {
	cred uint
	host string
	dir  string
}

type agentIdentityEntry struct {
	id      uint
	name    string
	kind    string
	version string
	hookAt  time.Time
	mcpAt   time.Time
	queryAt time.Time
	wroteAt time.Time
}

type agentSessionKey struct {
	identity uint
	conv     string
}

type agentSessionEntry struct {
	id          uint
	snap        agentstate.Snapshot
	workflow    string
	kind        string
	version     string
	persistedAt time.Time
}

type agentBinding struct {
	host string
	dir  string
	at   time.Time
	miss bool
}

// agentStateMem 是状态核心的内存部分（见 Store.agent）。零值可用。
type agentStateMem struct {
	mu         sync.Mutex
	identities map[agentIdentityKey]*agentIdentityEntry
	sessions   map[agentSessionKey]*agentSessionEntry
	binds      map[string]agentBinding
	runAuth    map[agentRunKey]time.Time // 运行权威标记：到这个时间之前，这个对话的状态由运行输出说了算（见 SetAgentRunAuthority）
	owners     map[string]agentOwner     // 对话认领：Hub 发起的运行清楚这个对话属于哪个工作流（见 ClaimConversation）
	ingested   int
	lastPrune  time.Time
	cfg        agentstate.Config
	cfgSet     bool
}

// agentRunKey 标识「某个工作流里的某个对话」。
type agentRunKey struct {
	wf   string
	conv string
}

// agentOwner 是一条对话认领：到 until 之前，这个对话属于 wf。
type agentOwner struct {
	wf    string
	until time.Time
}

func (m *agentStateMem) init() {
	if m.identities == nil {
		m.identities = make(map[agentIdentityKey]*agentIdentityEntry)
		m.sessions = make(map[agentSessionKey]*agentSessionEntry)
		m.binds = make(map[string]agentBinding)
		m.runAuth = make(map[agentRunKey]time.Time)
	}
	if m.owners == nil {
		m.owners = make(map[string]agentOwner)
	}
	if !m.cfgSet {
		m.cfg, m.cfgSet = agentstate.DefaultConfig(), true
	}
}

// normalizeProjectDir 规整项目目录：去首尾空白与末尾分隔符（根目录保留），最长 512 个字符。
func normalizeProjectDir(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if r := []rune(p); len(r) > 512 {
		p = string(r[:512])
	}
	if t := strings.TrimRight(p, `/\`); t != "" {
		return t
	}
	return p[:1]
}

// AgentIngestInput 是一次汇报（hook 事件，或 Hub 自己产生的事件）。
type AgentIngestInput struct {
	WorkflowID       string
	CredentialID     uint
	HostName         string // 连接凭据 / 请求锁定的主机名；空就用工作流上记录的绑定
	ProjectDirectory string // 调用方自报的项目目录；空就用工作流上记录的绑定
	ConversationID   string
	ClientKind       string
	CursorVersion    string
	Event            agentstate.Event
	// Latest 为 true 表示「记到这个工作流最近活动的那个会话上」：Hub 自己产生的事件（托管终端的屏幕扫描器）不知道
	// 对话 ID、凭据，也不该凭空造出一个新身份。找不到可归属的会话就什么都不记（res.IdentityID 为 0）。
	Latest bool
	// Conversation 为 true 表示「记到这个工作流里 ConversationID 这个对话的会话上」（不管它有没有结束）：
	// Hub 读到非交互运行的输出时清楚自己在续哪个对话——最近活动的会话可能是另一个，这个对话也可能刚被 hook 记成了 ended。
	// 同样不造新身份：这个对话还没有任何 hook 汇报过，就什么都不记（res.IdentityID 为 0）。
	Conversation bool
}

// AgentIngestResult 是 IngestAgentEvent 的结果。IdentityID 为 0 表示认不出身份（没有项目目录），什么都没记。
type AgentIngestResult struct {
	IdentityID uint
	SessionID  uint
	NewSession bool
	Change     agentstate.Change
	Snapshot   agentstate.Snapshot
}

// agentBindingForLocked 取工作流上记录的主机与项目目录（最近一次汇报会话的），带缓存。调用方持有 agent.mu。
func (s *Store) agentBindingForLocked(ctx context.Context, workflowID string, now time.Time) agentBinding {
	m := &s.agent
	if b, ok := m.binds[workflowID]; ok {
		ttl := agentBindingTTL
		if b.miss {
			ttl = agentBindingMissTTL
		}
		if now.Sub(b.at) < ttl {
			return b
		}
	}
	b := agentBinding{at: now}
	var sess model.FeedbackSession
	err := s.db.WithContext(ctx).Select("host_name", "project_directory").
		Where("workflow_id = ? AND project_directory <> ''", workflowID).
		Order("updated_at DESC").Limit(1).Take(&sess).Error
	if err == nil {
		b.host, b.dir = strings.TrimSpace(sess.HostName), normalizeProjectDir(sess.ProjectDirectory)
	}
	b.miss = b.dir == ""
	if len(m.binds) > agentMemMax {
		m.binds = make(map[string]agentBinding)
	}
	m.binds[workflowID] = b
	return b
}

// WorkflowBinding 返回工作流上记录的主机与项目目录（最近一个带目录的反馈会话的）；没有记录就是两个空串。
// 网页新建的占位行没有目录，不算数；智能体自己的汇报才带主机与目录。
func (s *Store) WorkflowBinding(ctx context.Context, workflowID string) (host, dir string) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return "", ""
	}
	m := &s.agent
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	b := s.agentBindingForLocked(ctx, wf, time.Now())
	return b.host, b.dir
}

// agentIdentityKeyLocked 决定这次汇报属于哪个身份。认不出项目目录就返回 ok=false：
// 与其把不同项目混进同一个「未知项目」，不如先不记；之后哪次调用带了目录，就开始记。
func (s *Store) agentIdentityKeyLocked(ctx context.Context, wf string, cred uint, host, dir string, now time.Time) (agentIdentityKey, bool) {
	host, dir = strings.TrimSpace(host), normalizeProjectDir(dir)
	if dir == "" || host == "" {
		b := s.agentBindingForLocked(ctx, wf, now)
		if dir == "" {
			dir = b.dir
		}
		if host == "" {
			host = b.host
		}
	} else {
		s.agent.binds[wf] = agentBinding{host: host, dir: dir, at: now}
	}
	if dir == "" {
		return agentIdentityKey{}, false
	}
	if len(host) > 128 {
		host = host[:128]
	}
	return agentIdentityKey{cred: cred, host: host, dir: dir}, true
}

type agentTouch struct {
	hook, mcp, query bool
	kind, version    string
}

// touchIdentityLocked 取（必要时创建）身份，并记下这次活动。库里的 last_* 字段按 agentIdentityWriteEvery 节流写，
// 客户端类型 / 版本变化则立刻写。调用方持有 agent.mu。
func (s *Store) touchIdentityLocked(ctx context.Context, key agentIdentityKey, tc agentTouch, now time.Time) (*agentIdentityEntry, error) {
	m := &s.agent
	e := m.identities[key]
	if e == nil {
		var row model.AgentIdentity
		err := s.db.WithContext(ctx).
			Where("credential_id = ? AND host_name = ? AND project_directory = ?", key.cred, key.host, key.dir).
			Take(&row).Error
		switch {
		case err == nil:
		case errors.Is(err, gorm.ErrRecordNotFound):
			row = model.AgentIdentity{CredentialID: key.cred, HostName: key.host, ProjectDirectory: key.dir, FirstSeenAt: now, LastSeenAt: now}
			if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
		// wroteAt 留零值：新建（或刚从库里恢复）后的第一次触摸立刻把时间戳写下去，之后才按 agentIdentityWriteEvery 节流。
		e = &agentIdentityEntry{id: row.ID, name: row.Name, kind: row.ClientKind, version: row.CursorVersion}
		if row.LastHookAt != nil {
			e.hookAt = *row.LastHookAt
		}
		if row.LastMCPAt != nil {
			e.mcpAt = *row.LastMCPAt
		}
		if row.LastQueryAt != nil {
			e.queryAt = *row.LastQueryAt
		}
		if len(m.identities) > agentMemMax {
			m.identities = make(map[agentIdentityKey]*agentIdentityEntry)
		}
		m.identities[key] = e
	}

	changed := false
	if tc.kind != "" && tc.kind != e.kind && !(tc.kind == "unknown" && e.kind != "") {
		e.kind, changed = tc.kind, true
	}
	if tc.version != "" && tc.version != e.version {
		e.version, changed = tc.version, true
	}
	if tc.hook {
		e.hookAt = now
	}
	if tc.mcp {
		e.mcpAt = now
	}
	if tc.query {
		e.queryAt = now
	}
	if changed || now.Sub(e.wroteAt) >= agentIdentityWriteEvery {
		upd := map[string]any{"last_seen_at": now, "client_kind": e.kind, "cursor_version": e.version, "updated_at": now}
		if !e.hookAt.IsZero() {
			upd["last_hook_at"] = e.hookAt
		}
		if !e.mcpAt.IsZero() {
			upd["last_mcp_at"] = e.mcpAt
		}
		if !e.queryAt.IsZero() {
			upd["last_query_at"] = e.queryAt
		}
		if err := s.db.WithContext(ctx).Model(&model.AgentIdentity{}).Where("id = ?", e.id).Updates(upd).Error; err != nil {
			return e, err
		}
		e.wroteAt = now
	}
	return e, nil
}

func snapshotFromRow(r model.AgentSession) agentstate.Snapshot {
	return agentstate.Snapshot{
		State: agentstate.State(r.State), Sub: r.Sub, Since: r.StateSince, Source: agentstate.Source(r.StateSource),
		StartedAt: r.StartedAt, EndedAt: r.EndedAt, EndReason: r.EndReason,
		LastEventAt: r.LastEventAt, LastTool: r.LastTool, ToolStartedAt: r.ToolStartedAt,
		TurnStartedAt: r.TurnStartedAt, TurnEndedAt: r.TurnEndedAt,
	}
}

func sessionColumns(sn agentstate.Snapshot, wf, kind, version string, now time.Time) map[string]any {
	return map[string]any{
		"workflow_id": wf, "client_kind": kind, "cursor_version": version,
		"started_at": sn.StartedAt, "ended_at": sn.EndedAt, "end_reason": sn.EndReason,
		"state": string(sn.State), "sub": sn.Sub, "state_source": string(sn.Source), "state_since": sn.Since, "last_event_at": sn.LastEventAt,
		"last_tool": sn.LastTool, "tool_started_at": sn.ToolStartedAt,
		"turn_started_at": sn.TurnStartedAt, "turn_ended_at": sn.TurnEndedAt,
		"updated_at": now,
	}
}

// agentSessionLocked 取会话条目：内存里有就用；没有就从库里恢复；库里也没有就是一个新会话（id 为 0，写库时再建行）。
func (s *Store) agentSessionLocked(ctx context.Context, identityID uint, conv string) (*agentSessionEntry, error) {
	m := &s.agent
	key := agentSessionKey{identity: identityID, conv: conv}
	if e := m.sessions[key]; e != nil {
		return e, nil
	}
	e := &agentSessionEntry{}
	var row model.AgentSession
	err := s.db.WithContext(ctx).Where("identity_id = ? AND conversation_id = ?", identityID, conv).Take(&row).Error
	switch {
	case err == nil:
		e.id, e.snap, e.workflow, e.kind, e.version, e.persistedAt = row.ID, snapshotFromRow(row), row.WorkflowID, row.ClientKind, row.CursorVersion, row.UpdatedAt
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return nil, err
	}
	if len(m.sessions) > agentMemMax {
		cut := time.Now().Add(-6 * time.Hour)
		for k, v := range m.sessions {
			if v.snap.LastEventAt.Before(cut) {
				delete(m.sessions, k)
			}
		}
		if len(m.sessions) > agentMemMax {
			m.sessions = make(map[agentSessionKey]*agentSessionEntry)
		}
	}
	m.sessions[key] = e
	return e, nil
}

func agentEventExtra(ch agentstate.Change, ev agentstate.Event) string {
	x := map[string]string{}
	if ch.Note != "" {
		x["note"] = ch.Note
	}
	switch ev.Name {
	case agentstate.EvStop, agentstate.EvSessionEnd, agentstate.EvChatEnd:
		if ev.Status != "" {
			x["status"] = ev.Status
		}
	}
	if len(x) == 0 {
		return ""
	}
	raw, err := json.Marshal(x)
	if err != nil {
		return ""
	}
	return string(raw)
}

// latestAgentSessionLocked 找工作流最近活动、且还没结束的那个会话，返回它的身份条目与对话 ID。
// 先看内存（hook 事件刚推进过的会话都在），再退到库里最近一行；都没有就 ok=false。调用方持有 agent.mu。
func (s *Store) latestAgentSessionLocked(ctx context.Context, wf string, now time.Time) (*agentIdentityEntry, string, bool, error) {
	m := &s.agent
	var (
		best    *agentSessionEntry
		bestKey agentSessionKey
	)
	for k, e := range m.sessions {
		if e.workflow != wf || e.snap.State == "" || e.snap.State == agentstate.StateEnded {
			continue
		}
		if best == nil || e.snap.LastEventAt.After(best.snap.LastEventAt) {
			best, bestKey = e, k
		}
	}
	if best == nil {
		var row model.AgentSession
		err := s.db.WithContext(ctx).Where("workflow_id = ? AND state <> '' AND state <> ?", wf, string(agentstate.StateEnded)).
			Order("last_event_at DESC, id DESC").Limit(1).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", false, nil
		}
		if err != nil {
			return nil, "", false, err
		}
		bestKey = agentSessionKey{identity: row.IdentityID, conv: row.ConversationID}
	}
	e, ok, err := s.identityEntryByIDLocked(ctx, bestKey.identity, now)
	if !ok {
		return nil, "", false, err
	}
	return e, bestKey.conv, true, err
}

// conversationSessionLocked 找工作流里这个对话 ID 的会话（不管有没有结束；几个身份都记过它就取最近活动的），返回它的身份条目。
// 先看内存，再退到库里；都没有（这个对话还没有任何汇报）就 ok=false。调用方持有 agent.mu。
//
// anyWorkflow 为真表示这个工作流已经认领了这个对话（见 ClaimConversation）：对话编号全局唯一，认领者说它是自己的，
// 就连「被别的工作流名下的 hook 汇报建出来的」会话也要认得出来（先找自己名下的，找不到才看别人名下的）。
// 没有认领时仍然只认这个工作流名下的——别的工作流里的同名对话不是它能碰的。
func (s *Store) conversationSessionLocked(ctx context.Context, wf, conv string, now time.Time, anyWorkflow bool) (*agentIdentityEntry, bool, error) {
	m := &s.agent
	var (
		best, bestAny       *agentSessionEntry
		bestKey, bestAnyKey agentSessionKey
	)
	for k, e := range m.sessions {
		if k.conv != conv || e.snap.State == "" {
			continue
		}
		switch {
		case e.workflow == wf:
			if best == nil || e.snap.LastEventAt.After(best.snap.LastEventAt) {
				best, bestKey = e, k
			}
		case anyWorkflow:
			if bestAny == nil || e.snap.LastEventAt.After(bestAny.snap.LastEventAt) {
				bestAny, bestAnyKey = e, k
			}
		}
	}
	if best == nil && bestAny != nil {
		best, bestKey = bestAny, bestAnyKey
	}
	if best == nil {
		var row model.AgentSession
		err := s.db.WithContext(ctx).Where("workflow_id = ? AND conversation_id = ? AND state <> ''", wf, conv).
			Order("last_event_at DESC, id DESC").Limit(1).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) && anyWorkflow {
			err = s.db.WithContext(ctx).Where("conversation_id = ? AND state <> ''", conv).
				Order("last_event_at DESC, id DESC").Limit(1).Take(&row).Error
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		bestKey = agentSessionKey{identity: row.IdentityID, conv: conv}
	}
	return s.identityEntryByIDLocked(ctx, bestKey.identity, now)
}

// ---- 对话认领（#182）----

// agentClaimMemMax 是内存里同时保留的认领条数上限（正常只有同时在跑的几个运行）；超过就先清过期的，再不行就清空。
const agentClaimMemMax = 512

// ownerLocked 返回认领这个对话的工作流；认领有时效，过期的顺手清掉。调用方持有 agent.mu。
func (m *agentStateMem) ownerLocked(conv string, now time.Time) (string, bool) {
	if conv == "" {
		return "", false
	}
	o, ok := m.owners[conv]
	if !ok {
		return "", false
	}
	if !now.Before(o.until) {
		delete(m.owners, conv)
		return "", false
	}
	return o.wf, true
}

// ConversationOwner 返回此刻认领这个对话的工作流（没有人认领，或认领已经过期就是空串）。
func (s *Store) ConversationOwner(conv string, now time.Time) string {
	conv = strings.TrimSpace(conv)
	if conv == "" {
		return ""
	}
	m := &s.agent
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	wf, _ := m.ownerLocked(conv, now)
	return wf
}

// ClaimConversation 认领一个对话：Hub 发起的非交互运行清楚自己在跑哪个对话（新开对话从输出里学到，续聊的在命令里），
// 也清楚这次运行是替哪个工作流跑的——而 hook 汇报带的工作流来自项目目录里的 .cursor/relaymesh.json，
// 同一个仓库的多个工作树共用一份配置时，它写的常常是另一个工作流。认领到 until 之前，这个对话的一切汇报
// （智能体状态会话、旧链路的对话编号与活动时间）都归 workflowID，不管汇报自己带的是哪个工作流。
//
// 认领的同时把已经建在别的工作流名下的状态会话（库里与内存里）搬过来，并清掉别的工作流上记着这个对话编号的旧「续聊目标」
// （见 dropForeignAgentLinks）；返回会话原来所在的工作流（去重，不含 workflowID），调用方据此通知 Web 重新取一遍。
// 同一个认领再来一次（运行监视每轮都会续期，收尾时缩短到收尾期）只改到期时间，不动库。空的工作流 / 对话编号、格式不对的对话编号一律忽略。
func (s *Store) ClaimConversation(ctx context.Context, workflowID, conv string, until, now time.Time) ([]string, error) {
	wf, conv := strings.TrimSpace(workflowID), strings.TrimSpace(conv)
	if wf == "" || !agentConvRe.MatchString(conv) {
		return nil, nil
	}
	m := &s.agent
	m.mu.Lock()
	m.init()
	if prev, ok := m.owners[conv]; ok && prev.wf == wf && now.Before(prev.until) {
		// 同一个认领者再来：只改到期时间（运行收尾时会缩短到「收尾期」，与运行权威同时失效），不动库。
		m.owners[conv] = agentOwner{wf: wf, until: until}
		m.mu.Unlock()
		return nil, nil
	}
	if len(m.owners) >= agentClaimMemMax {
		for k, o := range m.owners {
			if !now.Before(o.until) {
				delete(m.owners, k)
			}
		}
		if len(m.owners) >= agentClaimMemMax {
			m.owners = make(map[string]agentOwner)
		}
	}
	m.owners[conv] = agentOwner{wf: wf, until: until}
	from, err := s.rehomeConversationLocked(ctx, wf, conv, now)
	m.mu.Unlock()
	// 新的认领（或换了主人）才清旧链路：续期不会走到这里。放在释放状态核心的锁之后，避免两把锁嵌套。
	s.dropForeignAgentLinks(ctx, wf, conv)
	return from, err
}

// rehomeConversationLocked 把这个对话名下所有「工作流不是 wf」的状态会话归到 wf（库里一条语句，内存里逐条改）。调用方持有 agent.mu。
func (s *Store) rehomeConversationLocked(ctx context.Context, wf, conv string, now time.Time) ([]string, error) {
	var rows []model.AgentSession
	if err := s.db.WithContext(ctx).Select("id", "workflow_id").
		Where("conversation_id = ? AND workflow_id <> ?", conv, wf).Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var from []string
	for _, r := range rows {
		if r.WorkflowID != "" && !seen[r.WorkflowID] {
			seen[r.WorkflowID] = true
			from = append(from, r.WorkflowID)
		}
	}
	sort.Strings(from)
	var err error
	if len(rows) > 0 {
		err = s.db.WithContext(ctx).Model(&model.AgentSession{}).
			Where("conversation_id = ? AND workflow_id <> ?", conv, wf).
			Updates(map[string]any{"workflow_id": wf, "updated_at": now}).Error
	}
	if err == nil {
		for k, e := range s.agent.sessions {
			if k.conv == conv && e.workflow != wf {
				e.workflow = wf
			}
		}
		if len(rows) > 0 {
			log.Printf("[agent] 对话 %s 已被工作流 %s 认领：%d 个状态会话从 %s 归了过来", shortID(conv), wf, len(rows), strings.Join(from, "、"))
		}
	}
	return from, err
}

// rehomeSessionLocked 把一条已有的状态会话归到 wf（事件到来时发现它在别的工作流名下，而认领者是 wf）。调用方持有 agent.mu。
func (s *Store) rehomeSessionLocked(ctx context.Context, sess *agentSessionEntry, wf string, now time.Time) error {
	if sess == nil || sess.workflow == wf {
		return nil
	}
	if sess.id != 0 {
		if err := s.db.WithContext(ctx).Model(&model.AgentSession{}).Where("id = ?", sess.id).
			Updates(map[string]any{"workflow_id": wf, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	sess.workflow = wf
	return nil
}

// shortID 把对话编号缩成日志里好认的前 8 位。
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8] + "…"
	}
	return id
}

// identityEntryByIDLocked 按身份编号取内存里的身份条目（没有就从库里恢复）。身份行不存在就 ok=false。调用方持有 agent.mu。
func (s *Store) identityEntryByIDLocked(ctx context.Context, id uint, now time.Time) (*agentIdentityEntry, bool, error) {
	for _, e := range s.agent.identities {
		if e.id == id {
			return e, true, nil
		}
	}
	var idRow model.AgentIdentity
	if err := s.db.WithContext(ctx).Take(&idRow, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	e, err := s.touchIdentityLocked(ctx, agentIdentityKey{cred: idRow.CredentialID, host: idRow.HostName, dir: idRow.ProjectDirectory}, agentTouch{}, now)
	if e == nil {
		return nil, false, err
	}
	return e, true, err
}

// SetAgentRunAuthority 标记（或撤销）「这个对话正由 Hub 读非交互运行的输出判定状态」，直到 until：
// 期间 hook 的汇报只留记录、不改状态——运行输出里的事件是确定的，而 hook 在非交互模式下会漏报 stop、退出时还会报一个 sessionEnd。
// until 为零值表示撤销。标记到期自动失效：监视协程没来得及收尾（进程崩了、主机失联）也不会永远挡住 hook。
func (s *Store) SetAgentRunAuthority(workflowID, conv string, until time.Time) {
	wf, conv := strings.TrimSpace(workflowID), strings.TrimSpace(conv)
	if wf == "" || conv == "" {
		return
	}
	m := &s.agent
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	k := agentRunKey{wf: wf, conv: conv}
	if until.IsZero() {
		delete(m.runAuth, k)
		return
	}
	if len(m.runAuth) >= agentMemMax { // 正常只有同时在跑的几个；兜底防止异常增长
		m.runAuth = make(map[agentRunKey]time.Time)
	}
	m.runAuth[k] = until
}

// runAuthorityLocked 报告这个对话此刻是否由运行输出说了算；过期的标记顺手清掉。调用方持有 agent.mu。
func (m *agentStateMem) runAuthorityLocked(wf, conv string, now time.Time) bool {
	if conv == "" {
		return false
	}
	k := agentRunKey{wf: wf, conv: conv}
	until, ok := m.runAuth[k]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(m.runAuth, k)
		return false
	}
	return true
}

// IngestAgentEvent 把一次汇报折进状态核心。没有项目目录（认不出身份）时什么都不记，返回零值结果。
// 写库失败只丢掉持久化：内存里的状态照常前进，下一次事件会重试写库。
func (s *Store) IngestAgentEvent(ctx context.Context, in AgentIngestInput) (AgentIngestResult, error) {
	var res AgentIngestResult
	wf := strings.TrimSpace(in.WorkflowID)
	if wf == "" || in.Event.Name == "" {
		return res, nil
	}
	ev := in.Event
	if ev.Received.IsZero() {
		ev.Received = time.Now()
	}
	// 事件表里不留自由文本：工具名与状态都收敛到安全字符集 / 已知取值（调用方清洗过一遍，这里再兜一层）。
	ev.Tool = agentstate.SanitizeTool(ev.Tool)
	switch ev.Name {
	case agentstate.EvStop:
		ev.Status = agentstate.NormalizeStopStatus(ev.Status)
	case agentstate.EvSessionEnd:
		ev.Status = agentstate.NormalizeEndReason(ev.Status, agentstate.EndClosed)
	case agentstate.EvChatEnd:
		ev.Status = agentstate.NormalizeEndReason(ev.Status, agentstate.EndCompleted)
	default:
		ev.Status = ""
	}
	conv := strings.TrimSpace(in.ConversationID)
	if !agentConvRe.MatchString(conv) {
		conv = ""
	}
	kind, version := agentstate.NormalizeClientKind(in.ClientKind), agentstate.NormalizeVersion(in.CursorVersion)

	m := &s.agent
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()

	// 对话认领（#182）：Hub 发起的运行认领了这个对话，它的汇报一律归认领者——hook 带的工作流来自项目目录里的
	// .cursor/relaymesh.json，同一个仓库的多个工作树共用一份配置时，写的常常是另一个工作流。
	claimed := false
	if owner, ok := m.ownerLocked(conv, ev.Received); ok {
		wf, claimed = owner, true
	}

	var (
		idEntry *agentIdentityEntry
		err     error
	)
	switch {
	case in.Conversation:
		var ok bool
		if conv == "" {
			return res, nil
		}
		if idEntry, ok, err = s.conversationSessionLocked(ctx, wf, conv, ev.Received, claimed); !ok {
			return res, err
		}
	case in.Latest:
		var ok bool
		if idEntry, conv, ok, err = s.latestAgentSessionLocked(ctx, wf, ev.Received); !ok {
			return res, err
		}
	default:
		key, ok := s.agentIdentityKeyLocked(ctx, wf, in.CredentialID, in.HostName, in.ProjectDirectory, ev.Received)
		if !ok {
			return res, nil
		}
		idEntry, err = s.touchIdentityLocked(ctx, key, agentTouch{hook: ev.Source == agentstate.SourceHook, kind: kind, version: version}, ev.Received)
		if idEntry == nil {
			return res, err
		}
	}
	res.IdentityID = idEntry.id
	sess, serr := s.agentSessionLocked(ctx, idEntry.id, conv)
	if serr != nil {
		return res, serr
	}
	// 认领者说了算：这个对话的会话如果建在了别的工作流名下，先归过来——下面的「运行权威」分支会提前返回，
	// 不在这里纠正，会话就永远卡在原来的工作流里（#182 现场：hook 配置改绑之后，事件全被当作只留记录的汇报吞掉）。
	if claimed && sess.id != 0 && sess.workflow != wf {
		if rerr := s.rehomeSessionLocked(ctx, sess, wf, ev.Received); rerr != nil && err == nil {
			err = rerr
		}
	}
	// 会话的客户端类型 / 版本：这次没报就沿用会话已有的，再没有就取身份上最好的；报了 unknown 而已知是 ide / cli 就保持已知的。
	if kind == "" || (kind == "unknown" && sess.kind != "" && sess.kind != "unknown") {
		kind = sess.kind
	}
	if kind == "" {
		kind = idEntry.kind
	}
	if version == "" {
		version = sess.version
	}
	if version == "" {
		version = idEntry.version
	}

	// 这个对话正由 Hub 读运行输出判定状态：hook 的汇报只留一条记录（思考心跳太密，不留），不改状态也不写会话行。
	// 会话还不存在（sess.id == 0）时照常处理：运行权威只管已经存在的会话，别因此吞掉一个对话的第一次汇报。
	if ev.Source == agentstate.SourceHook && sess.id != 0 && m.runAuthorityLocked(wf, conv, ev.Received) {
		ch := agentstate.Change{At: ev.Received, Record: ev.Name != agentstate.EvThought, Note: agentstate.NoteRunAuthority}
		res.Change, res.Snapshot, res.SessionID = ch, sess.snap, sess.id
		if ch.Record {
			rec := model.AgentEventRecord{
				IdentityID: idEntry.id, SessionID: sess.id, At: ev.Received, Source: string(ev.Source), Event: ev.Name,
				Tool: ev.Tool, DurationMS: int(ev.Duration / time.Millisecond), Extra: agentEventExtra(ch, ev),
			}
			if werr := s.db.WithContext(ctx).Create(&rec).Error; werr != nil && err == nil {
				err = werr
			}
		}
		return res, err
	}

	isNew := sess.id == 0
	snap, ch := agentstate.Apply(sess.snap, ev, m.cfg)
	res.Change, res.Snapshot, res.NewSession = ch, snap, isNew
	if snap.State == "" { // 事件没有有效内容，新会话也不建行
		return res, err
	}
	sess.snap = snap

	writeSession := isNew || ch.Durable || sess.workflow != wf || sess.kind != kind || sess.version != version ||
		ev.Received.Sub(sess.persistedAt) >= agentSessionHeartbeatEvery
	if writeSession || ch.Record {
		werr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if writeSession {
				if sess.id == 0 {
					row := model.AgentSession{IdentityID: idEntry.id, ConversationID: conv, CreatedAt: ev.Received}
					if err := tx.Create(&row).Error; err != nil {
						return err
					}
					sess.id = row.ID
				}
				if err := tx.Model(&model.AgentSession{}).Where("id = ?", sess.id).
					Updates(sessionColumns(snap, wf, kind, version, ev.Received)).Error; err != nil {
					return err
				}
			}
			if ch.Record && sess.id != 0 {
				at := ch.At
				if at.IsZero() {
					at = ev.Received
				}
				rec := model.AgentEventRecord{
					IdentityID: idEntry.id, SessionID: sess.id, At: at, Source: string(ev.Source), Event: ev.Name,
					Tool: ev.Tool, DurationMS: int(ev.Duration / time.Millisecond), Extra: agentEventExtra(ch, ev),
				}
				if rec.Source == "" {
					rec.Source = string(agentstate.SourceHook)
				}
				return tx.Create(&rec).Error
			}
			return nil
		})
		if werr == nil && writeSession {
			sess.persistedAt, sess.workflow, sess.kind, sess.version = ev.Received, wf, kind, version
		} else if werr != nil && err == nil {
			err = werr
		}
	}
	res.SessionID = sess.id

	m.ingested++
	if m.ingested >= agentPruneEveryIngests || ev.Received.Sub(m.lastPrune) > agentPruneEvery {
		m.ingested, m.lastPrune = 0, ev.Received
		s.pruneAgentStateLocked(ctx, ev.Received)
	}
	return res, err
}

// pruneAgentStateLocked 清理过期数据：事件流水保留 7 天且不超过 2 万行；90 天没有动静的会话连同它的事件一起删。
func (s *Store) pruneAgentStateLocked(ctx context.Context, now time.Time) {
	db := s.db.WithContext(ctx)
	_ = db.Where("at < ?", now.Add(-agentEventsKeepFor)).Delete(&model.AgentEventRecord{}).Error
	var cutoffID uint
	if err := db.Model(&model.AgentEventRecord{}).Select("id").Order("id DESC").Offset(agentEventsKeepRows).Limit(1).Scan(&cutoffID).Error; err == nil && cutoffID > 0 {
		_ = db.Where("id <= ?", cutoffID).Delete(&model.AgentEventRecord{}).Error
	}
	var stale []uint
	if err := db.Model(&model.AgentSession{}).Where("last_event_at < ?", now.Add(-agentSessionsKeepFor)).Limit(500).Pluck("id", &stale).Error; err == nil && len(stale) > 0 {
		_ = db.Where("session_id IN ?", stale).Delete(&model.AgentEventRecord{}).Error
		_ = db.Where("id IN ?", stale).Delete(&model.AgentSession{}).Error
		for k, v := range s.agent.sessions {
			for _, id := range stale {
				if v.id == id {
					delete(s.agent.sessions, k)
					break
				}
			}
		}
	}
}

// AgentTouchInput 是一次 MCP 调用（带工作流编号）留下的痕迹。
type AgentTouchInput struct {
	WorkflowID       string
	CredentialID     uint
	HostName         string
	ProjectDirectory string
	Query            bool // 读类调用（收件箱、会话列表、文档读取……）
	// Hook 为真表示这是编辑器 hook 的轮询（没带运行情况事件的那种）：只记「hook 最近一次查询」，不算智能体调用 RelayMesh。
	Hook          bool
	ClientKind    string // hook 顺带汇报的客户端类型（ide / cli），其余场景留空
	CursorVersion string
}

// TouchAgentIdentity 记下某个身份刚刚调用过 RelayMesh（以及是不是读类调用）。只更新身份的时间戳，不产生事件；
// 认不出项目目录就什么都不记。每个 MCP 调用都会走到这里，所以先查内存，写库按 agentIdentityWriteEvery 节流。
func (s *Store) TouchAgentIdentity(ctx context.Context, in AgentTouchInput, now time.Time) error {
	wf := strings.TrimSpace(in.WorkflowID)
	if wf == "" {
		return nil
	}
	m := &s.agent
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	key, ok := s.agentIdentityKeyLocked(ctx, wf, in.CredentialID, in.HostName, in.ProjectDirectory, now)
	if !ok {
		return nil
	}
	tc := agentTouch{mcp: true, query: in.Query}
	if in.Hook {
		tc = agentTouch{hook: true, kind: agentstate.NormalizeClientKind(in.ClientKind), version: agentstate.NormalizeVersion(in.CursorVersion)}
	}
	_, err := s.touchIdentityLocked(ctx, key, tc, now)
	return err
}

// ---- 读取与改名 ----

// AgentIdentityView 是对外的身份视图。
type AgentIdentityView struct {
	ID               uint       `json:"id"`
	CredentialID     uint       `json:"credential_id"`
	HostName         string     `json:"host_name"`
	ProjectDirectory string     `json:"project_directory"`
	Name             string     `json:"name"`
	DirectoryName    string     `json:"directory_name"`
	Label            string     `json:"label"`
	LabelDegraded    bool       `json:"label_degraded"`
	ClientKind       string     `json:"client_kind,omitempty"`
	CursorVersion    string     `json:"cursor_version,omitempty"`
	FirstSeenAt      time.Time  `json:"first_seen_at"`
	LastSeenAt       time.Time  `json:"last_seen_at"`
	LastHookAt       *time.Time `json:"last_hook_at,omitempty"`
	LastMCPAt        *time.Time `json:"last_mcp_at,omitempty"`
	LastQueryAt      *time.Time `json:"last_query_at,omitempty"`
}

func identityViews(rows []model.AgentIdentity) []AgentIdentityView {
	inputs := make([]agentstate.LabelInput, 0, len(rows))
	for _, r := range rows {
		inputs = append(inputs, agentstate.LabelInput{ID: r.ID, HostName: r.HostName, ProjectDirectory: r.ProjectDirectory, Name: r.Name})
	}
	labels := agentstate.Labels(inputs)
	out := make([]AgentIdentityView, 0, len(rows))
	for _, r := range rows {
		l := labels[r.ID]
		out = append(out, AgentIdentityView{
			ID: r.ID, CredentialID: r.CredentialID, HostName: r.HostName, ProjectDirectory: r.ProjectDirectory,
			Name: r.Name, DirectoryName: agentstate.DirectoryName(r.ProjectDirectory), Label: l.Text, LabelDegraded: l.Degraded,
			ClientKind: r.ClientKind, CursorVersion: r.CursorVersion, FirstSeenAt: r.FirstSeenAt, LastSeenAt: r.LastSeenAt,
			LastHookAt: r.LastHookAt, LastMCPAt: r.LastMCPAt, LastQueryAt: r.LastQueryAt,
		})
	}
	return out
}

// laterTime 返回「库里的时间」与「内存里的时间」中较晚的那个（内存零值 = 没有）；两边都没有时保持 nil。
func laterTime(db *time.Time, mem time.Time) *time.Time {
	if mem.IsZero() {
		return db
	}
	if db == nil || mem.After(*db) {
		t := mem
		return &t
	}
	return db
}

// overlayIdentityLiveLocked 把内存里更新的时间戳盖到库里读出的身份视图上（调用方须持有 s.agent.mu）。
//
// 库里的 last_hook_at / last_mcp_at / last_query_at 按 agentIdentityWriteEvery 节流写，直接读库会落后最多 20 秒：
// 身份卡上「最后查询」明明刚刚发生过，却显示成「—」或一个旧时间。内存里的才是最新的
// （Hub 重启后内存从库里恢复，所以不会比库更旧）。
func (s *Store) overlayIdentityLiveLocked(views []AgentIdentityView) {
	m := &s.agent
	if len(m.identities) == 0 || len(views) == 0 {
		return
	}
	byID := make(map[uint]*agentIdentityEntry, len(m.identities))
	for _, e := range m.identities {
		byID[e.id] = e
	}
	for i := range views {
		e := byID[views[i].ID]
		if e == nil {
			continue
		}
		v := &views[i]
		v.LastHookAt = laterTime(v.LastHookAt, e.hookAt)
		v.LastMCPAt = laterTime(v.LastMCPAt, e.mcpAt)
		v.LastQueryAt = laterTime(v.LastQueryAt, e.queryAt)
		for _, t := range []*time.Time{v.LastHookAt, v.LastMCPAt, v.LastQueryAt} {
			if t != nil && t.After(v.LastSeenAt) {
				v.LastSeenAt = *t
			}
		}
	}
}

// liveIdentityViews 是读取路径上统一的「库里的身份 + 内存里的最新时间戳」。
func (s *Store) liveIdentityViews(rows []model.AgentIdentity) []AgentIdentityView {
	views := identityViews(rows)
	m := &s.agent
	m.mu.Lock()
	defer m.mu.Unlock()
	s.overlayIdentityLiveLocked(views)
	return views
}

// ListAgentIdentities 返回全部身份（最近活动的在前），显示名按「改名 ?? 目录名、重名降级」统一计算。
func (s *Store) ListAgentIdentities(ctx context.Context) ([]AgentIdentityView, error) {
	var rows []model.AgentIdentity
	if err := s.db.WithContext(ctx).Order("last_seen_at DESC, id DESC").Limit(1000).Find(&rows).Error; err != nil {
		return nil, err
	}
	return s.liveIdentityViews(rows), nil
}

// RenameAgentIdentity 给身份改名（空串 = 恢复用目录名）。改名写一条审计事件，不改变身份编号。
func (s *Store) RenameAgentIdentity(ctx context.Context, id uint, name, by string) (*AgentIdentityView, error) {
	name = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	name = strings.TrimSpace(name)
	if len([]rune(name)) > AgentNameMaxRunes {
		return nil, NewInvalidInputError("name is too long (max 80 characters)")
	}

	m := &s.agent
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()

	var row model.AgentIdentity
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("agent identity not found")
		}
		return nil, err
	}
	if row.Name != name {
		now := time.Now()
		extra, _ := json.Marshal(map[string]string{"from": row.Name, "to": name, "by": by})
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.AgentIdentity{}).Where("id = ?", id).Updates(map[string]any{"name": name, "updated_at": now}).Error; err != nil {
				return err
			}
			return tx.Create(&model.AgentEventRecord{IdentityID: id, At: now, Source: string(agentstate.SourceWeb), Event: "rename", Extra: string(extra)}).Error
		})
		if err != nil {
			return nil, err
		}
		row.Name = name
		for _, e := range m.identities {
			if e.id == id {
				e.name = name
			}
		}
	}
	var all []model.AgentIdentity
	if err := s.db.WithContext(ctx).Order("last_seen_at DESC, id DESC").Limit(1000).Find(&all).Error; err != nil {
		return nil, err
	}
	views := identityViews(all)
	s.overlayIdentityLiveLocked(views) // 本函数已持有 m.mu
	for _, v := range views {
		if v.ID == id {
			v := v
			return &v, nil
		}
	}
	one := identityViews([]model.AgentIdentity{row})
	s.overlayIdentityLiveLocked(one)
	return &one[0], nil
}

// AgentSessionFilter 是 ListAgentSessions 的过滤条件。
type AgentSessionFilter struct {
	WorkflowID string
	IdentityID uint
	Limit      int // 默认 20，最多 200
}

// AgentSessionView 是对外的会话视图：库里的状态加上 Evaluate 推导出的展示状态。
type AgentSessionView struct {
	ID             uint   `json:"id"`
	IdentityID     uint   `json:"identity_id"`
	WorkflowID     string `json:"workflow_id"`
	ConversationID string `json:"conversation_id"`
	ClientKind     string `json:"client_kind,omitempty"`
	CursorVersion  string `json:"cursor_version,omitempty"`

	IdentityLabel string `json:"identity_label"`
	HostName      string `json:"host_name"`
	ProjectDir    string `json:"project_directory"`

	StartedAt     *time.Time `json:"started_at,omitempty"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	EndReason     string     `json:"end_reason,omitempty"`
	LastTool      string     `json:"last_tool,omitempty"`
	ToolStartedAt *time.Time `json:"tool_started_at,omitempty"`
	TurnStartedAt *time.Time `json:"turn_started_at,omitempty"`
	TurnEndedAt   *time.Time `json:"turn_ended_at,omitempty"`
	LastEventAt   time.Time  `json:"last_event_at"`
	LastMCPAt     *time.Time `json:"last_mcp_at,omitempty"`
	LastQueryAt   *time.Time `json:"last_query_at,omitempty"`

	State             string    `json:"state"`      // 展示态（含 waiting_you / offline）
	BaseState         string    `json:"base_state"` // 状态机里的状态
	Sub               string    `json:"sub,omitempty"`
	StateSource       string    `json:"state_source,omitempty"` // hook | tui | run：现在这个状态是谁说的（空 = 旧数据，没有记录）
	Since             time.Time `json:"since"`
	QuietSeconds      int       `json:"quiet_seconds"`
	WaitingYou        bool      `json:"waiting_you"`
	Stalled           bool      `json:"stalled"`
	StalledSoft       bool      `json:"stalled_soft"`
	ApprovalSuspected bool      `json:"approval_suspected"`
	ApprovalOverdue   bool      `json:"approval_overdue"`
	Reason            string    `json:"reason,omitempty"`

	// 托管终端的屏幕信号（M3.2）。TUI 为空表示这个会话没有可用的屏幕扫描结果。
	TUI          string     `json:"tui,omitempty"`           // ok | unrecognized
	LiveWord     string     `json:"live_word,omitempty"`     // 状态行动词（动画中才有）
	TokensLive   *int       `json:"tokens_live,omitempty"`   // 状态行里的实时 token 数
	TokensSource string     `json:"tokens_source,omitempty"` // tui
	CtxPct       *float64   `json:"ctx_pct,omitempty"`       // 页脚的上下文占用百分比
	ScreenAt     *time.Time `json:"screen_at,omitempty"`     // 屏幕信号的接收时间
}

func tptr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// waitingYouWorkflows 返回给定工作流里「有待答复的常规汇报」的那些（Agent 正阻塞等人回答）。
func (s *Store) waitingYouWorkflows(ctx context.Context, workflows []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(workflows) == 0 {
		return out, nil
	}
	var ids []string
	if err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
		Where("workflow_id IN ? AND status = 'pending' AND "+notNoticeSQL, workflows).
		Distinct().Pluck("workflow_id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// ListAgentSessions 返回会话的展示状态（最近有动静的在前）。now 由调用方给，便于测试。
func (s *Store) ListAgentSessions(ctx context.Context, f AgentSessionFilter, now time.Time) ([]AgentSessionView, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	q := s.db.WithContext(ctx).Model(&model.AgentSession{})
	if wf := strings.TrimSpace(f.WorkflowID); wf != "" {
		q = q.Where("workflow_id = ?", wf)
	}
	if f.IdentityID != 0 {
		q = q.Where("identity_id = ?", f.IdentityID)
	}
	var rows []model.AgentSession
	if err := q.Order("last_event_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []AgentSessionView{}, nil
	}

	var idRows []model.AgentIdentity
	if err := s.db.WithContext(ctx).Order("last_seen_at DESC, id DESC").Limit(1000).Find(&idRows).Error; err != nil {
		return nil, err
	}
	idViews := s.liveIdentityViews(idRows)
	byID := make(map[uint]AgentIdentityView, len(idViews))
	for _, v := range idViews {
		byID[v.ID] = v
	}
	wfSet := map[string]bool{}
	var wfList []string
	for _, r := range rows {
		if r.WorkflowID != "" && !wfSet[r.WorkflowID] {
			wfSet[r.WorkflowID] = true
			wfList = append(wfList, r.WorkflowID)
		}
	}
	waiting, err := s.waitingYouWorkflows(ctx, wfList)
	if err != nil {
		return nil, err
	}

	// 每个工作流只有「最近活动的那个会话」归屏幕信号（托管终端里跑的就是它）。
	liveRow := map[string]uint{}
	for _, r := range rows {
		if r.WorkflowID == "" || r.State == string(agentstate.StateEnded) {
			continue
		}
		if cur, ok := liveRow[r.WorkflowID]; !ok || r.ID == cur {
			liveRow[r.WorkflowID] = r.ID
		}
	}
	th := agentstate.DefaultThresholds()
	out := make([]AgentSessionView, 0, len(rows))
	for _, r := range rows {
		id := byID[r.IdentityID]
		var mcpAt time.Time
		if id.LastMCPAt != nil {
			mcpAt = *id.LastMCPAt
		}
		if t, ok := s.AgentLastActivity(r.WorkflowID); ok && t.After(mcpAt) {
			mcpAt = t
		}
		in := agentstate.EvalInput{
			Snap: snapshotFromRow(r), Now: now, LastMCPAt: mcpAt, WaitingYou: waiting[r.WorkflowID],
		}
		var scr *AgentScreenView
		if liveRow[r.WorkflowID] == r.ID {
			if scr = s.agentScreenOf(r.WorkflowID, now); scr != nil {
				in.HasLiveTerminal = true
				if scr.TUI == "ok" {
					sig := scr.signal
					in.Screen = &sig
				}
			}
		}
		v := agentstate.Evaluate(in, th)
		view := AgentSessionView{
			ID: r.ID, IdentityID: r.IdentityID, WorkflowID: r.WorkflowID, ConversationID: r.ConversationID,
			ClientKind: r.ClientKind, CursorVersion: r.CursorVersion,
			IdentityLabel: id.Label, HostName: id.HostName, ProjectDir: id.ProjectDirectory,
			StartedAt: tptr(r.StartedAt), EndedAt: tptr(r.EndedAt), EndReason: r.EndReason, LastTool: r.LastTool,
			ToolStartedAt: tptr(r.ToolStartedAt), TurnStartedAt: tptr(r.TurnStartedAt), TurnEndedAt: tptr(r.TurnEndedAt),
			LastEventAt: r.LastEventAt, LastMCPAt: tptr(mcpAt), LastQueryAt: id.LastQueryAt,
			State: string(v.State), BaseState: string(v.Base), Sub: v.Sub, StateSource: r.StateSource, Since: v.Since,
			QuietSeconds: int(v.QuietFor / time.Second), WaitingYou: v.WaitingYou, Stalled: v.Stalled,
			StalledSoft: v.StalledSoft, ApprovalSuspected: v.ApprovalSuspected, ApprovalOverdue: v.ApprovalOverdue, Reason: v.Reason,
		}
		if scr != nil {
			view.TUI, view.LiveWord, view.ScreenAt = scr.TUI, scr.Word, tptr(scr.ReceivedAt)
			if scr.Tokens >= 0 {
				tk := scr.Tokens
				view.TokensLive, view.TokensSource = &tk, scr.TokensSrc
			}
			if scr.CtxPct >= 0 {
				cp := scr.CtxPct
				view.CtxPct = &cp
			}
		}
		out = append(out, view)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastEventAt.After(out[j].LastEventAt) })
	return out, nil
}
