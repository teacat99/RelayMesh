package api

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func waitStreamEnd(t *testing.T, body io.Reader, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, body)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s：SSE 连接没有在期限内结束", what)
	}
}

func TestSSEBrokerCloseEndsOpenAndFutureStreams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	broker := NewSSEBroker()
	engine := gin.New()
	engine.GET("/events", broker.HandleSSE)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()

	// 先确认连接已建立（收到 connected 事件），之后的结束才是 Close 引起的。
	br := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	connected := false
	for time.Now().Before(deadline) && !connected {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read before close: %v", err)
		}
		connected = strings.Contains(line, "connected")
	}
	if !connected {
		t.Fatal("没有收到 connected 事件")
	}

	broker.Close()
	waitStreamEnd(t, br, "关停时已连接的流")

	// 关停之后新接入的连接也应当立刻结束，而不是再挂住一轮超时。
	late, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("late connect: %v", err)
	}
	defer late.Body.Close()
	waitStreamEnd(t, late.Body, "关停后新接入的流")

	broker.Close() // 可重复调用，不应 panic
}
