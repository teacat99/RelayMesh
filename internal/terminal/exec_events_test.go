package terminal

import "testing"

// 实时输出片段在每个订阅者的队列里只占有限的位置：输出洪水不会让一个停住的查看者吃掉大量内存，
// 也不会把状态事件的位置占满；状态事件真的放不下时，订阅者才会被断开（它会重连并重新拉取快照）。
func TestExecBusOutputQueueIsBoundedAndKeepsRoomForState(t *testing.T) {
	b := newExecBus()
	sub, ok := b.subscribe()
	if !ok {
		t.Fatal("应能订阅")
	}
	defer b.unsubscribe(sub)

	for i := 0; i < execSubQueue*2; i++ {
		b.publishOut("run", "stdout", uint64(i), []byte("x"))
	}
	if got := len(sub.ch); got != execOutQueue {
		t.Fatalf("实时输出片段最多占 %d 个位置，实际排了 %d 个", execOutQueue, got)
	}
	if b.count() != 1 {
		t.Fatalf("输出片段放不下只是被丢弃，不应断开订阅者")
	}

	// 剩下的位置都留给状态事件。
	for i := 0; i < execSubQueue-execOutQueue; i++ {
		b.publishPause(i%2 == 0)
	}
	if b.count() != 1 {
		t.Fatalf("状态事件应该放得下，订阅者不应被断开")
	}
	// 再多一个就放不下了：断开这个跟不上的订阅者。
	b.publishPause(true)
	if b.count() != 0 {
		t.Fatalf("状态事件放不下时应断开订阅者")
	}
}
