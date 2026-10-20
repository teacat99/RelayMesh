package store

import (
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestComputeAgentLive(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	hook := now.Add(-1 * time.Minute)

	cases := []struct {
		name string
		link AgentLink
		hook time.Time
		ok   bool
		sess *model.FeedbackSession
		want AgentLiveState
	}{
		{"unknown 无 hook", AgentLink{}, time.Time{}, false, nil, AgentLiveUnknown},
		{"running 工具事件", AgentLink{LastToolAt: now.Add(-30 * time.Second)}, hook, true, nil, AgentLiveRunning},
		{"idle 正常 stop", AgentLink{StopAt: now.Add(-10 * time.Second), StopStatus: StopCompleted, LastToolAt: now.Add(-2 * time.Minute)}, hook, true, nil, AgentLiveIdle},
		{"quiet 执行中无 hook", AgentLink{LastToolAt: now.Add(-2 * time.Minute)}, now.Add(-5 * time.Minute), true, &model.FeedbackSession{Status: "completed", ConsumedByAI: true}, AgentLiveQuiet},
	}
	for _, c := range cases {
		got := ComputeAgentLive(c.link, c.hook, c.ok, c.sess, now)
		if got.State != c.want {
			t.Fatalf("%s: want %s got %s (%s)", c.name, c.want, got.State, got.Reason)
		}
	}
}
