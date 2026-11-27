package service_test

import (
	"context"
	"testing"

	agentrepository "github.com/biox-dev/gobrave/internal/agent/application/repository"
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// TestGetSessionIncludesActiveTask 校验会话详情一次性带出当前轮次状态快照，
// 前端无需再按 taskId 单独查询任务即可判定运行态。
func TestGetSessionIncludesActiveTask(t *testing.T) {
	ctx := context.Background()
	chat, svc, sess, sessions, _ := newTurnTestChat(t)
	tasks := agentrepository.NewGormTaskRepository(turnTestDB(t, svc))

	// 无活跃轮次：不返回快照。
	detail, err := chat.GetSession(ctx, sess.ID, agentservice.SessionMessageQuery{})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if detail.ActiveTask != nil {
		t.Fatalf("ActiveTask = %+v, want nil", detail.ActiveTask)
	}

	// 运行中：快照带出 status。
	task := seedTurnTask(t, svc, sess.ID)
	sess.CurrentTaskID = task.ID
	if _, err := sessions.Save(ctx, sess); err != nil {
		t.Fatalf("save session: %v", err)
	}
	task.Status = agenttypes.TaskRunning
	if err := tasks.Update(ctx, task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	detail, err = chat.GetSession(ctx, sess.ID, agentservice.SessionMessageQuery{})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if detail.ActiveTask == nil {
		t.Fatalf("ActiveTask = nil, want snapshot")
	}
	if detail.ActiveTask.Status != agenttypes.TaskRunning {
		t.Fatalf("status = %q, want %q", detail.ActiveTask.Status, agenttypes.TaskRunning)
	}

	// failed（中断态）：resumable 为真，error 一并带出（供前端补错误气泡）。
	task.Status = agenttypes.TaskFailed
	task.Error = "boom"
	if err := tasks.Update(ctx, task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	detail, err = chat.GetSession(ctx, sess.ID, agentservice.SessionMessageQuery{})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if detail.ActiveTask == nil || !detail.ActiveTask.Resumable {
		t.Fatalf("ActiveTask = %+v, want resumable", detail.ActiveTask)
	}
	if detail.ActiveTask.Error != "boom" {
		t.Fatalf("error = %q, want boom", detail.ActiveTask.Error)
	}
}

// TestGetSessionActiveTaskMissingForStalePointer 校验任务行缺失时快照为 nil，
// 前端据 current_task_id 判定「状态未知」而不是误判为已结束。
func TestGetSessionActiveTaskMissingForStalePointer(t *testing.T) {
	ctx := context.Background()
	chat, _, sess, sessions, _ := newTurnTestChat(t)

	sess.CurrentTaskID = sess.ID + 999 // 指向不存在的任务
	if _, err := sessions.Save(ctx, sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	detail, err := chat.GetSession(ctx, sess.ID, agentservice.SessionMessageQuery{})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if detail.ActiveTask != nil {
		t.Fatalf("ActiveTask = %+v, want nil", detail.ActiveTask)
	}
	if detail.CurrentTaskID == 0 {
		t.Fatalf("CurrentTaskID should stay non-zero")
	}
}

// TestListSessionsBackfillsActiveTaskStatus 校验会话列表批量回填当前轮次状态：
// 只回填一次任务表，且 canceled（保留指针可续跑）不再被前端误判为运行中。
func TestListSessionsBackfillsActiveTaskStatus(t *testing.T) {
	ctx := context.Background()
	chat, svc, sess, sessions, _ := newTurnTestChat(t)
	tasks := agentrepository.NewGormTaskRepository(turnTestDB(t, svc))

	task := seedTurnTask(t, svc, sess.ID)
	sess.CurrentTaskID = task.ID
	if _, err := sessions.Save(ctx, sess); err != nil {
		t.Fatalf("save session: %v", err)
	}
	task.Status = agenttypes.TaskCanceled
	if err := tasks.Update(ctx, task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	list, total, err := chat.ListSessions(ctx, sess.ProjectID, "", 0, 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("total = %d, len = %d, want 1/1", total, len(list))
	}
	if list[0].ActiveTaskStatus != agenttypes.TaskCanceled {
		t.Fatalf("ActiveTaskStatus = %q, want %q", list[0].ActiveTaskStatus, agenttypes.TaskCanceled)
	}
}
