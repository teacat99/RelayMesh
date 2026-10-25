package oauth

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	oauthEndpointLimitPerMin = 60
	oauthPendingPerIPClient  = 20
	oauthPendingGlobalMax    = 5000
)

type oauthRateLimiter struct {
	mu    sync.Mutex
	hits  map[string][]time.Time
	pend  map[string]int
	pendG int
}

func newOAuthRateLimiter() *oauthRateLimiter {
	return &oauthRateLimiter{
		hits: make(map[string][]time.Time),
		pend: make(map[string]int),
	}
}

func (rl *oauthRateLimiter) allowEndpoint(ip string, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	key := "ep:" + ip
	list := rl.trim(rl.hits[key], now.Add(-time.Minute))
	if len(list) >= oauthEndpointLimitPerMin {
		rl.hits[key] = list
		return false
	}
	rl.hits[key] = append(list, now)
	return true
}

func (rl *oauthRateLimiter) trim(list []time.Time, cut time.Time) []time.Time {
	j := 0
	for _, t := range list {
		if t.After(cut) {
			list[j] = t
			j++
		}
	}
	return list[:j]
}

func (rl *oauthRateLimiter) reservePending(ip, clientID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.pendG >= oauthPendingGlobalMax {
		return false
	}
	k := ip + "|" + clientID
	if rl.pend[k] >= oauthPendingPerIPClient {
		return false
	}
	rl.pend[k]++
	rl.pendG++
	return true
}

// releasePending 归还 reservePending 占的一个名额。
//
// 名额的归还由待决请求自己的生命周期驱动（见 memoryStore：批准 / 拒绝 / 过期 / 客户端被删除都会走到这里），
// 每个待决请求只会归还一次；这里仍然按「这个 key 确实占着名额」来判断，没占着就什么都不做，
// 避免多还一次把全局计数拉低、让上限形同虚设。归零的 key 同时从表里删掉，不让表随历史上出现过的
// IP+客户端组合无限增长。
func (rl *oauthRateLimiter) releasePending(ip, clientID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	k := ip + "|" + clientID
	n := rl.pend[k]
	if n <= 0 {
		return
	}
	if n == 1 {
		delete(rl.pend, k)
	} else {
		rl.pend[k] = n - 1
	}
	if rl.pendG > 0 {
		rl.pendG--
	}
}

func writeOAuthRateLimited(c *gin.Context, retryAfterSec int) {
	c.Header("Retry-After", strconv.Itoa(retryAfterSec))
	c.JSON(http.StatusTooManyRequests, gin.H{"error": "slow_down"})
}
