package mcp

import (
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 用户在场计时（#121「收尾宽限」）。
//
// 问题：智能体以为沟通已经结束就收尾，而用户其实只是还在打字 / 还没想好。服务端只能看到「挂起倒计时走完了」，
// 分不清「用户走了」和「用户还在」。这里让 Web 把「我还在」明确告诉服务端：
//   - plus：用户点一下「+1 分钟」，这次挂起多挂 60 秒（可连点累加）；
//   - typing：Web 检测到正在输入时每几秒发一次心跳；心跳期间倒计时暂停（累计的暂停时长加给这次挂起），
//     停下来后倒计时从暂停处接着走，不会一停手就立刻超时。
//
// 只作用于「正挂着的这次长轮询」；没有智能体在挂着时没有可延的东西，调用方得到 false。
// 状态只存在内存里，随这次挂起结束而消失，不需要落库。

const (
	// typingFreshness 超过这个间隔没有新的输入心跳就算停手了。
	typingFreshness = 10 * time.Second
	// presencePlusStep 每次「+1 分钟」。
	presencePlusStep = time.Minute
	// presenceMaxExtra 一次挂起最多被延长多久，防止无限续命把智能体永远挂住。
	presenceMaxExtra = 30 * time.Minute
)

// PresencePlus 给 sessionID 当前的挂起加一分钟，返回加完后这次挂起的总时长（秒）；没有人在挂着时返回 false。
func (s *Server) PresencePlus(sessionID string) (totalSec int, ok bool) {
	s.waitersMu.Lock()
	list := s.waiters[sessionID]
	if len(list) == 0 {
		s.waitersMu.Unlock()
		return 0, false
	}
	w := &list[len(list)-1]
	w.extra += presencePlusStep
	if w.extra > presenceMaxExtra {
		w.extra = presenceMaxExtra
	}
	totalSec = w.timeoutSec + int(w.extra/time.Second)
	s.waitersMu.Unlock()
	s.notifySessionUpdate(sessionID)
	return totalSec, true
}

// PresenceTyping 记一次「正在输入」心跳：把距上一次心跳的间隔（不超过新鲜期）补进 extra，倒计时就等于暂停了。
func (s *Server) PresenceTyping(sessionID string) (totalSec int, ok bool) {
	now := time.Now()
	s.waitersMu.Lock()
	list := s.waiters[sessionID]
	if len(list) == 0 {
		s.waitersMu.Unlock()
		return 0, false
	}
	w := &list[len(list)-1]
	if !w.lastTick.IsZero() {
		if gap := now.Sub(w.lastTick); gap > 0 && gap <= typingFreshness {
			w.extra += gap
		}
	}
	w.lastTick = now
	if w.extra > presenceMaxExtra {
		w.extra = presenceMaxExtra
	}
	totalSec = w.timeoutSec + int(w.extra/time.Second)
	s.waitersMu.Unlock()
	return totalSec, true
}

// holdRemaining 返回这次挂起（按 ch 识别）还应继续挂多久；<=0 表示可以发空回执了。
// 用户正在输入时（心跳新鲜）不会超时：至少再等一个新鲜期，等心跳停下后倒计时接着走。
func (s *Server) holdRemaining(sessionID string, ch chan *model.FeedbackSession) time.Duration {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()
	for _, w := range s.waiters[sessionID] {
		if w.ch != ch {
			continue
		}
		now := time.Now()
		left := w.startedAt.Add(time.Duration(w.timeoutSec)*time.Second + w.extra).Sub(now)
		if !w.lastTick.IsZero() && now.Sub(w.lastTick) <= typingFreshness {
			if wait := typingFreshness - now.Sub(w.lastTick) + time.Second; wait > left {
				left = wait
			}
		}
		return left
	}
	return 0
}
