package api

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type SSEBroker struct {
	clientsMu sync.Mutex
	clients   map[chan string]bool
}

func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients: make(map[chan string]bool),
	}
}

func (b *SSEBroker) Broadcast(eventType string, data any) {
	b.clientsMu.Lock()
	defer b.clientsMu.Unlock()

	var dataStr string
	if str, ok := data.(string); ok {
		dataStr = str
	} else {
		bytes, _ := json.Marshal(data)
		dataStr = string(bytes)
	}

	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, dataStr)

	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (b *SSEBroker) HandleSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	ch := make(chan string, 10)
	b.clientsMu.Lock()
	b.clients[ch] = true
	b.clientsMu.Unlock()

	defer func() {
		b.clientsMu.Lock()
		delete(b.clients, ch)
		close(ch)
		b.clientsMu.Unlock()
	}()

	// Send initial client reconnection instruction and connected event
	c.Writer.Write([]byte("retry: 3000\n\n"))
	c.SSEvent("connected", map[string]string{"status": "ok"})
	c.Writer.Flush()

	// 15 seconds periodic heartbeat ticker to keep alive through reverse proxies
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false
		case <-heartbeat.C:
			// 双通道心跳保障：
			// 1. 标准 SSE 注释行（: ping\n\n），维持反向代理与网关 TCP 长连接活跃
			w.Write([]byte(": ping\n\n"))
			// 2. 具名标准事件（event: ping\ndata: keepalive\n\n），被浏览器 EventSource 监听切实刷新前端看门狗
			c.SSEvent("ping", "keepalive")
			return true
		case msg, ok := <-ch:
			if !ok {
				return false
			}
			w.Write([]byte(msg))
			return true
		}
	})
}
