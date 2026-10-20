package chat

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Key 是消息在流里的完整排序键：（发送毫秒，来源序，主键）。
//
// 主键是来源表主键的字符串形式；数字主键补零到 12 位，字典序才与数值序一致。
// 同一毫秒内多条消息的先后由来源序再主键裁决，保证翻页稳定、不重不漏。
type Key struct {
	Ms   int64
	Rank int
	ID   string
}

// Less 报告 k 是否排在 o 之前。
func (k Key) Less(o Key) bool {
	if k.Ms != o.Ms {
		return k.Ms < o.Ms
	}
	if k.Rank != o.Rank {
		return k.Rank < o.Rank
	}
	return k.ID < o.ID
}

// Equal 报告两个键是否相同。
func (k Key) Equal(o Key) bool { return k.Ms == o.Ms && k.Rank == o.Rank && k.ID == o.ID }

// numKey 把数字主键补零成固定宽度的字符串，使字典序等于数值序。
func numKey(id uint) string { return fmt.Sprintf("%012d", id) }

// Encode 生成不透明游标（url 安全的 base64，不带填充）。
func (k Key) Encode() string {
	raw := strconv.FormatInt(k.Ms, 10) + "." + strconv.Itoa(k.Rank) + "." + k.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ErrBadCursor 表示游标不是本服务生成的（被截断、被篡改或来自别的会话类型）。
var ErrBadCursor = errors.New("游标无效")

// ParseCursor 解析 Encode 生成的游标。
func ParseCursor(s string) (Key, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Key{}, ErrBadCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Key{}, ErrBadCursor
	}
	parts := strings.SplitN(string(raw), ".", 3)
	if len(parts) != 3 {
		return Key{}, ErrBadCursor
	}
	ms, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || ms < 0 {
		return Key{}, ErrBadCursor
	}
	rank, err := strconv.Atoi(parts[1])
	if err != nil || rank < 0 || rank > 100 {
		return Key{}, ErrBadCursor
	}
	if parts[2] == "" {
		return Key{}, ErrBadCursor
	}
	return Key{Ms: ms, Rank: rank, ID: parts[2]}, nil
}
