package service

import (
	"sync"
	"time"

	"github.com/google/uuid"

	. "github.com/biox-dev/gobrave/internal/agent/types"
)

// 本文件维护「会话的实时尾巴」：当前正在流式输出、但尚未写入 agent_messages 的内容。
//
// 为什么需要它：增量（text / reasoning_delta）只广播不落库，而完整块（message /
// reasoning）要等一整轮迭代结束才发出（见 native agent 的 streamOnce）。这段时间里
// 刷新页面或中途进入会话，历史只有到上一个完整块为止，整段回答直接丢失。
//
// 设计取舍：
//   - 只缓存「尚未落库」的内容：增量累积成打开中的段，完整块一到就从缓存里移除，
//     因此内存占用约等于当前这一段未落库文本，不随会话长度增长；
//   - 每次 stream 事件分配会话内递增的 Seq：订阅时把 Seq 一起下发，客户端据此丢弃
//     「快照已包含」的重复推送（增量与快照可能以任意顺序到达客户端）。

// LiveBlock 是一段尚未落库的流式内容。
type LiveBlock struct {
	// ID 是段的稳定标识（创建时生成）：客户端用它作列表 key，快照与后续增量共用。
	ID      string `json:"id"`
	Kind    string `json:"kind"` // KindAssistant / KindReasoning
	Content string `json:"content"`
	TaskID  int64  `json:"task_id,string"`
	// Streaming 恒为 true：缓存里只有「未落库」的段。
	Streaming bool `json:"streaming"`
}

// SessionLive 是会话当前实时尾巴的快照（只包含未入库内容）。
type SessionLive struct {
	SessionID int64 `json:"session_id,string"`
	// TaskID 是这些内容所属的任务：客户端只对同一任务应用 Seq 水位线。
	TaskID int64       `json:"task_id,string"`
	Seq    int64       `json:"seq"`
	Blocks []LiveBlock `json:"blocks"`
}

// sessionLiveState 是单个会话的缓存状态；同一会话的轮次串行执行，因此无需按任务分桶。
type sessionLiveState struct {
	taskID    int64
	seq       int64
	blocks    []LiveBlock
	updatedAt time.Time
}

const (
	// liveIdleTTL 是会话缓存（含 Seq 水位线）的最长空闲时间：任务异常退出、没有终态
	// 事件时兜底回收，避免进程长期运行积累无用条目。
	liveIdleTTL = 30 * time.Minute
	// liveSweepInterval 是回收扫描间隔（只在有事件时顺带执行，不额外起 goroutine）。
	liveSweepInterval = 5 * time.Minute
)

type liveTail struct {
	mu        sync.Mutex
	sessions  map[int64]*sessionLiveState
	lastSweep time.Time
}

func newLiveTail() *liveTail {
	return &liveTail{sessions: make(map[int64]*sessionLiveState), lastSweep: time.Now()}
}

// Observe 折叠一条 stream 事件并返回会话内递增序号。
//
// 只关心「内容何时离开未落库状态」：
//   - text / reasoning_delta：追加到对应类型的打开段；
//   - message / reasoning：完整块已写入 agent_messages，丢弃对应类型的段；
//   - done / error：本轮结束，清空全部段（历史才是权威）。
func (t *liveTail) Observe(sessionID, taskID int64, event StreamEvent) int64 {
	if t == nil || sessionID <= 0 {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.sweepLocked(now)

	st := t.sessions[sessionID]
	if st == nil {
		st = &sessionLiveState{taskID: taskID, updatedAt: now}
		t.sessions[sessionID] = st
	}
	if st.taskID != taskID {
		// 会话内开始新一轮：上一轮的尾巴不再属于当前任务。
		st.taskID = taskID
		st.blocks = nil
	}
	st.seq++
	st.updatedAt = now

	switch event.Type {
	case StreamEventText:
		st.append(KindAssistant, event.Content)
	case StreamEventReasoningDelta:
		st.append(KindReasoning, event.Content)
	case StreamEventMessage:
		st.drop(KindAssistant)
	case StreamEventReasoning:
		st.drop(KindReasoning)
	case StreamEventDone, StreamEventError:
		st.blocks = nil
	}
	return st.seq
}

// EndTask 在任务进入终态时清空未落库内容（此时历史已经或即将完整）。
func (t *liveTail) EndTask(sessionID, taskID int64) {
	if t == nil || sessionID <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.sessions[sessionID]
	if st == nil || (taskID > 0 && st.taskID != taskID) {
		return
	}
	st.blocks = nil
	st.updatedAt = time.Now()
}

// Snapshot 返回会话当前的未落库内容；没有内容时 Blocks 为空。
func (t *liveTail) Snapshot(sessionID int64) SessionLive {
	out := SessionLive{SessionID: sessionID}
	if t == nil || sessionID <= 0 {
		return out
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.sessions[sessionID]
	if st == nil {
		return out
	}
	out.TaskID = st.taskID
	out.Seq = st.seq
	if len(st.blocks) > 0 {
		out.Blocks = append([]LiveBlock(nil), st.blocks...)
	}
	return out
}

// Clear 丢弃某个会话的全部缓存（会话删除 / 进程级清理）。
func (t *liveTail) Clear(sessionID int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessions, sessionID)
}

func (t *liveTail) sweepLocked(now time.Time) {
	if now.Sub(t.lastSweep) < liveSweepInterval {
		return
	}
	t.lastSweep = now
	for id, st := range t.sessions {
		if now.Sub(st.updatedAt) > liveIdleTTL {
			delete(t.sessions, id)
		}
	}
}

// append 把增量追加到最后一个同类型的打开段；没有则新开一段。
func (st *sessionLiveState) append(kind, delta string) {
	if delta == "" {
		return
	}
	for i := len(st.blocks) - 1; i >= 0; i-- {
		if st.blocks[i].Kind != kind {
			continue
		}
		st.blocks[i].Content += delta
		return
	}
	st.blocks = append(st.blocks, LiveBlock{
		ID:        uuid.NewString(),
		Kind:      kind,
		Content:   delta,
		TaskID:    st.taskID,
		Streaming: true,
	})
}

// drop 移除某类型的全部段（该内容已经落库）。
func (st *sessionLiveState) drop(kind string) {
	if len(st.blocks) == 0 {
		return
	}
	kept := st.blocks[:0]
	for _, b := range st.blocks {
		if b.Kind == kind {
			continue
		}
		kept = append(kept, b)
	}
	st.blocks = kept
}
