package terminal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"
)

// 拉取票据：pull_prepare 签发，凭它可以下载「签发时已经授权过的那一个文件」一次。
//
// 票据是持有即有效的凭证，所以：
//   - 只在内存里，且只存它的 SHA-256（内存转储或调试输出里没有可直接使用的票据）；
//   - 一次性：取走即作废，下载失败也不能重放，需要重新 pull_prepare；
//   - 120 秒内有效，总量与每令牌数量都有上限；
//   - 令牌被修改 / 停用 / 删除 / 重新生成时，它名下的票据立即作废；
//   - 任何无效情况（不存在、已用、已过期、格式不对）对外都是同一种应答，不泄露区别。

const (
	fileTicketTTL     = 120 * time.Second
	fileTicketMax     = 256
	fileTicketPerCred = 16
	fileTicketBytes   = 32
	// fileTicketLen 是票据文本（base64url，无填充）的长度。
	fileTicketLen = 43
)

// fileTicket 是一张票据背后的授权快照：下载时用它重新向 Agent 打开同一个文件。
type fileTicket struct {
	CredentialID uint
	Credential   string
	Machine      string
	Path         string
	Roots        []string
	Size         int64
	Expires      time.Time
	IP           string
}

type fileTickets struct {
	now func() time.Time

	mu sync.Mutex
	m  map[[32]byte]*fileTicket
}

func newFileTickets(now func() time.Time) *fileTickets {
	return &fileTickets{now: now, m: make(map[[32]byte]*fileTicket)}
}

func ticketKey(token string) [32]byte { return sha256.Sum256([]byte(token)) }

// purgeLocked 清掉过期票据。
func (t *fileTickets) purgeLocked(now time.Time) {
	for k, v := range t.m {
		if !now.Before(v.Expires) {
			delete(t.m, k)
		}
	}
}

// issue 签发一张票据。总量或该令牌的未使用票据已达上限时返回 ok=false。
func (t *fileTickets) issue(tk fileTicket) (token string, ok bool) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.purgeLocked(now)
	if len(t.m) >= fileTicketMax {
		return "", false
	}
	perCred := 0
	for _, v := range t.m {
		if v.CredentialID == tk.CredentialID {
			perCred++
		}
	}
	if perCred >= fileTicketPerCred {
		return "", false
	}
	raw := make([]byte, fileTicketBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", false
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	tk.Expires = now.Add(fileTicketTTL)
	tk.Roots = append([]string(nil), tk.Roots...)
	t.m[ticketKey(token)] = &tk
	return token, true
}

// take 取走一张票据（一次性）。任何无效情况都返回 nil。
func (t *fileTickets) take(token string) *fileTicket {
	if len(token) != fileTicketLen {
		return nil
	}
	if dec, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(dec) != fileTicketBytes {
		return nil
	}
	key := ticketKey(token)
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	tk := t.m[key]
	if tk == nil {
		return nil
	}
	delete(t.m, key)
	if !now.Before(tk.Expires) {
		return nil
	}
	return tk
}

// revokeCredential 作废某个令牌名下的全部票据，返回作废的张数。
func (t *fileTickets) revokeCredential(id uint) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for k, v := range t.m {
		if v.CredentialID == id {
			delete(t.m, k)
			n++
		}
	}
	return n
}

// count 返回当前有效票据数（测试与状态用）。
func (t *fileTickets) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.purgeLocked(t.now())
	return len(t.m)
}
