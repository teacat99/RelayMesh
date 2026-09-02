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

	return &Store{db: db}, nil
}

func (s *Store) DB() *gorm.DB {
	return s.db
}

func (s *Store) WithTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.WithContext(ctx).Transaction(fn)
}
