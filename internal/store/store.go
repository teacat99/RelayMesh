package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/glebarez/sqlite"
	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store struct {
	db *gorm.DB
	mu sync.Mutex
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

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
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
		&model.UserNorm{},
		&model.MCPCredential{},
		&model.WorkflowPhaseState{},
		&model.WorkflowCheckpoint{},
		&model.WorkflowNote{},
	); err != nil {
		return nil, fmt.Errorf("failed to automigrate database tables: %w", err)
	}

	// 存量老数据自愈：自动为历史遗留的空 workflow_id 会话补齐规范的 wf-{id} 标识（如 sess-e61b8709 自动自愈为 wf-e61b8709）
	_ = db.Model(&model.FeedbackSession{}).
		Where("workflow_id IS NULL OR workflow_id = ''").
		Updates(map[string]interface{}{
			"workflow_id": gorm.Expr("'wf-' || replace(id, 'sess-', '')"),
		}).Error

	return &Store{db: db}, nil
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
