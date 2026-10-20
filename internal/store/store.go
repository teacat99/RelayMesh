package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/glebarez/sqlite"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store struct {
	db *gorm.DB
	mu sync.Mutex

	// activity 记录各工作流的智能体最近一次调用 RelayMesh 的时间（只在内存里，见 inbox.go）。
	activity agentActivity
	// inboxCfg 缓存收件箱开关（见 inbox_config.go），免得每次工具调用都读库。
	inboxCfg inboxConfigCache
	// sweep 记录上次清理过期追加的时间（见 inbox.go）。
	sweep inboxSweep
	// agent 是 Agent 身份与状态核心的内存部分（见 agent_state.go）。
	agent   agentStateMem
	screens agentScreenMem // 托管终端屏幕信号（易失，见 agent_screen.go）
}

func New(dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create database directory %s: %w", dir, err)
			}
		}
	}

	dsn := dbPath
	if dbPath != ":memory:" {
		dsn = fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	}

	// 默认静默；只有设置了 RELAYMESH_SQL_TRACE（实验与排查用，见 sqltrace.go）才把每条 SQL 追加写入追踪文件。
	gormLogger := logger.Default.LogMode(logger.Silent)
	if p := os.Getenv(SQLTraceEnv); p != "" {
		tl, err := newSQLTraceLogger(p)
		if err != nil {
			return nil, fmt.Errorf("打开 SQL 追踪文件 %s 失败: %w", p, err)
		}
		gormLogger = tl
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}
	// SQLite single-writer optimization
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(
		&model.Task{},
		&model.Segment{},
		&model.Report{},
		&model.Feedback{},
		&model.FeedbackSession{},
		&model.SystemSetting{},
		&model.QueuedFeedback{},
		&model.WorkflowDraft{},
		&model.DraftImage{},
		&model.UserNorm{},
		&model.MCPCredential{},
		&model.OAuthClient{},
		&model.OAuthGrant{},
		&model.OAuthAccessToken{},
		&model.WorkflowPhaseState{},
		&model.WorkflowCheckpoint{},
		&model.WorkflowNote{},
		&model.TerminalHost{},
		&model.TerminalAuditLog{},
		&model.TerminalSession{},
		&model.TerminalRun{},
		&model.TerminalArchive{},
		&model.TerminalLostDismissal{},
		&model.WorkItem{},
		&model.WorkItemUpdate{},
		&model.WorkItemSession{},
		&model.WorkItemCounter{},
		&model.WorkBoardCounter{},
		&model.WorkItemAlias{},
		&model.WorkModule{},
		&model.ProjectDoc{},
		&model.ProjectDocRevision{},
		&model.ProjectDocCounter{},
		&model.Project{},
		&model.ProjectDir{},
		&model.ProjectModule{},
		&model.AuditEvent{},
		&model.ArtifactData{},
		&model.ArtifactComment{},
		&model.ACLGrant{},
		&model.ConfirmItem{},
		&model.ConfirmItemCounter{},
		&model.WorkflowMode{},
		&model.WorkflowHookSeen{},
		&model.AuthProfile{},
		&model.AgentIdentity{},
		&model.AgentSession{},
		&model.AgentEventRecord{},
		&model.ConversationRead{},
		&model.ConversationEvent{},
		&model.Conversation{},
		&model.ConversationMember{},
		&model.ChatMessage{},
	); err != nil {
		return nil, fmt.Errorf("failed to automigrate database tables: %w", err)
	}

	// 存量老数据自愈：自动为历史遗留的空 workflow_id 会话补齐规范的 wf-{id} 标识（如 sess-e61b8709 自动自愈为 wf-e61b8709）
	_ = db.Model(&model.FeedbackSession{}).
		Where("workflow_id IS NULL OR workflow_id = ''").
		Updates(map[string]interface{}{
			"workflow_id": gorm.Expr("'wf-' || replace(id, 'sess-', '')"),
		}).Error

	s := &Store{db: db}
	_ = s.SeedBuiltinNorms(context.Background())
	_ = s.SeedDefaultAuthProfile(context.Background())
	s.migratePhasesV3(context.Background())
	_ = s.BackfillHiddenOAuthCredentialTokens(context.Background())
	s.BackfillDecisionConfirmItems(context.Background())
	// 给没有 board_key 的历史任务 / 规划节点补板（#175）：幂等，没有待补的行时只做两次计数查询。
	s.reconcileBoardsAtStartup(context.Background())
	// hook 最近出现的时间装回内存：重启（含热更新）后异步对话不必等 hook 再来一次才恢复（#103）。
	// 装不回来就退回「从头记」的旧行为（保守、不出错），所以不当成启动失败。
	_ = s.RestoreHookSeen(context.Background())

	return s, nil
}

func (s *Store) DB() *gorm.DB {
	return s.db
}

// Close closes the underlying sql.DB connection.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (s *Store) WithTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.WithContext(ctx).Transaction(fn)
}
