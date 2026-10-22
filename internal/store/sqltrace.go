package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SQLTraceEnv 是开启 SQL 追踪的环境变量名（只在实验与排查里用，默认关闭，生产不设）。
//
// 设置为一个文件路径后，Store 执行的每一条 SQL 都以一行 JSON 追加写入该文件：开始时间、耗时（毫秒）、行数、报错与 SQL 原文
// （绑定值已代入；超过 sqlTraceMaxLen 的部分截断，免得把整段汇报正文写进去）。配套的离线分析工具是 scripts/dev/explain-audit：
// 把真实页面加载（跑浏览器套件）时发出的 SQL 按「形状」归并、统计次数与耗时，再逐个形状做 EXPLAIN QUERY PLAN，找全表扫描与临时排序（#177）。
//
// 注意：文件里是真实数据的片段，只用于临时库；不要对着含真实内容的库长期打开，用完删除。
const SQLTraceEnv = "RELAYMESH_SQL_TRACE"

// sqlTraceMaxLen 是追踪文件里单条 SQL 的最大长度（字符数）；INSERT / UPDATE 里的大段正文会被截掉，SELECT 的 WHERE 与 ORDER 都在前面。
const sqlTraceMaxLen = 4000

type sqlTraceLine struct {
	At   string  `json:"at"`
	MS   float64 `json:"ms"`
	Rows int64   `json:"rows"`
	Err  string  `json:"err,omitempty"`
	SQL  string  `json:"sql"`
}

// sqlTraceLogger 把 gorm 的每条 SQL 追加写入文件；实现 gorm 的 logger.Interface，只关心 Trace。
type sqlTraceLogger struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

func newSQLTraceLogger(path string) (*sqlTraceLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &sqlTraceLogger{f: f, enc: json.NewEncoder(f)}, nil
}

func (l *sqlTraceLogger) LogMode(logger.LogLevel) logger.Interface      { return l }
func (l *sqlTraceLogger) Info(context.Context, string, ...interface{})  {}
func (l *sqlTraceLogger) Warn(context.Context, string, ...interface{})  {}
func (l *sqlTraceLogger) Error(context.Context, string, ...interface{}) {}

func (l *sqlTraceLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, rows := fc()
	if r := []rune(sql); len(r) > sqlTraceMaxLen {
		sql = string(r[:sqlTraceMaxLen]) + "…[截断]"
	}
	line := sqlTraceLine{
		At:   begin.UTC().Format(time.RFC3339Nano),
		MS:   float64(time.Since(begin).Microseconds()) / 1000,
		Rows: rows,
		SQL:  sql,
	}
	// 「没找到」不是错误（First / Take 没有行时 gorm 也会报它），不写进 err 列
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		line.Err = err.Error()
	}
	l.mu.Lock()
	_ = l.enc.Encode(line)
	l.mu.Unlock()
}
