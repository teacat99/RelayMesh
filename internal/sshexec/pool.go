package sshexec

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Pool 按主机复用 SSH 连接，并限制每台主机上同时进行的会话数。
type Pool struct {
	DialTimeout time.Duration
	MaxSessions int           // 每台主机同时进行的会话数上限，默认 8
	IdleTimeout time.Duration // 连接空闲多久后被回收，默认 90 秒

	mu    sync.Mutex
	hosts map[string]*poolEntry
}

type poolEntry struct {
	dialMu sync.Mutex // 串行化建立连接（建立期间不持有 st，Forget 不会被拖住）
	st     sync.Mutex
	client *Client
	closed bool
	last   time.Time
	sem    chan struct{}
}

var errEntryClosed = errors.New("sshexec: 连接池条目已被丢弃")

func (p *Pool) dialTimeout() time.Duration {
	if p.DialTimeout > 0 {
		return p.DialTimeout
	}
	return DefaultDialTimeout
}

func (p *Pool) maxSessions() int {
	if p.MaxSessions > 0 {
		return p.MaxSessions
	}
	return 8
}

func (p *Pool) idleTimeout() time.Duration {
	if p.IdleTimeout > 0 {
		return p.IdleTimeout
	}
	return 90 * time.Second
}

func (p *Pool) entry(key string) *poolEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hosts == nil {
		p.hosts = make(map[string]*poolEntry)
	}
	e, ok := p.hosts[key]
	if !ok {
		e = &poolEntry{sem: make(chan struct{}, p.maxSessions())}
		p.hosts[key] = e
	}
	return e
}

func (e *poolEntry) cached() (*Client, bool) {
	e.st.Lock()
	defer e.st.Unlock()
	return e.client, e.closed
}

func (e *poolEntry) get(ctx context.Context, sp Spec, timeout time.Duration) (*Client, error) {
	e.dialMu.Lock()
	defer e.dialMu.Unlock()
	if c, closed := e.cached(); closed {
		return nil, errEntryClosed
	} else if c != nil {
		return c, nil
	}
	c, err := Dial(ctx, sp, timeout)
	if err != nil {
		return nil, err
	}
	e.st.Lock()
	if e.closed { // 建立连接期间被 Forget 了：不要留下泄漏的连接
		e.st.Unlock()
		_ = c.Close()
		return nil, errEntryClosed
	}
	e.client = c
	e.st.Unlock()
	go func() { // 连接断开时丢弃缓存，下一次调用会重新建立
		_ = c.wait()
		e.st.Lock()
		if e.client == c {
			e.client = nil
		}
		e.st.Unlock()
	}()
	return c, nil
}

func (e *poolEntry) drop(c *Client) {
	e.st.Lock()
	if e.client == c {
		e.client = nil
	}
	e.st.Unlock()
	_ = c.Close()
}

func (e *poolEntry) touch() {
	e.st.Lock()
	e.last = time.Now()
	e.st.Unlock()
}

// Exec 在 key 对应的主机上运行 cmd。连接层面的失败（连接被对端关掉等）会用新连接重试一次；
// 因此调用方传入的脚本必须是幂等的。
func (p *Pool) Exec(ctx context.Context, key string, sp Spec, cmd string, stdin []byte, maxOut int) (Result, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		e := p.entry(key)
		select {
		case e.sem <- struct{}{}:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		res, err := p.execOn(ctx, e, sp, cmd, stdin, maxOut)
		<-e.sem
		switch {
		case err == nil:
			return res, nil
		case errors.Is(err, errEntryClosed), errors.Is(err, ErrConn):
			lastErr = err
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			continue
		}
		return res, err
	}
	if errors.Is(lastErr, errEntryClosed) {
		return Result{}, ErrConn
	}
	return Result{}, lastErr
}

func (p *Pool) execOn(ctx context.Context, e *poolEntry, sp Spec, cmd string, stdin []byte, maxOut int) (Result, error) {
	c, err := e.get(ctx, sp, p.dialTimeout())
	if err != nil {
		return Result{}, err
	}
	res, err := c.Exec(ctx, cmd, stdin, maxOut)
	if err != nil && errors.Is(err, ErrConn) {
		e.drop(c)
		return res, err
	}
	e.touch()
	return res, err
}

// Forget 关闭并丢弃某台主机的连接（凭据 / 固定的主机密钥 / 地址变化，或主机被删除时调用）。
func (p *Pool) Forget(key string) {
	p.mu.Lock()
	e, ok := p.hosts[key]
	if ok {
		delete(p.hosts, key)
	}
	p.mu.Unlock()
	if !ok {
		return
	}
	e.st.Lock()
	e.closed = true
	c := e.client
	e.client = nil
	e.st.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// Reap 回收空闲超过 IdleTimeout 的连接，返回回收数量。
func (p *Pool) Reap(now time.Time) int {
	p.mu.Lock()
	entries := make([]*poolEntry, 0, len(p.hosts))
	for _, e := range p.hosts {
		entries = append(entries, e)
	}
	p.mu.Unlock()
	n := 0
	for _, e := range entries {
		if len(e.sem) > 0 {
			continue
		}
		e.st.Lock()
		c := e.client
		idle := c != nil && now.Sub(e.last) > p.idleTimeout()
		if idle {
			e.client = nil
		}
		e.st.Unlock()
		if idle {
			_ = c.Close()
			n++
		}
	}
	return n
}

// Close 关闭所有连接。
func (p *Pool) Close() {
	p.mu.Lock()
	keys := make([]string, 0, len(p.hosts))
	for k := range p.hosts {
		keys = append(keys, k)
	}
	p.mu.Unlock()
	for _, k := range keys {
		p.Forget(k)
	}
}
