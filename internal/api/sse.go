package api

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

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

	// Send initial connected event
	c.SSEvent("connected", map[string]string{"status": "ok"})
	c.Writer.Flush()

	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false
		case msg, ok := <-ch:
			if !ok {
				return false
			}
			w.Write([]byte(msg))
			return true
		}
	})
}
