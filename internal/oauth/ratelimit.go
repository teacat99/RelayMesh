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
	oauthPendingGlobalTTL    = 10 * time.Minute
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

func (rl *oauthRateLimiter) releasePending(ip, clientID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	k := ip + "|" + clientID
	if rl.pend[k] > 0 {
		rl.pend[k]--
	}
	if rl.pendG > 0 {
		rl.pendG--
	}
}

func writeOAuthRateLimited(c *gin.Context, retryAfterSec int) {
	c.Header("Retry-After", strconv.Itoa(retryAfterSec))
	c.JSON(http.StatusTooManyRequests, gin.H{"error": "slow_down"})
}
