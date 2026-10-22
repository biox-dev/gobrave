package agent

import (
	"context"
	"testing"

	"github.com/biox-dev/gobrave/internal/utils"
)

// fakeTaskRepository 是 TaskRepository 的最小内存实现，仅用于验证任务终态回调。
type fakeTaskRepository struct {
	tasks map[int64]*Task
}

func newFakeTaskRepository() *fakeTaskRepository {
	return &fakeTaskRepository{tasks: make(map[int64]*Task)}
}

func (f *fakeTaskRepository) Create(_ context.Context, task *Task) error {
	f.tasks[task.ID] = task
	return nil
}

func (f *fakeTaskRepository) Get(_ context.Context, id int64) (*Task, error) {
	if task, ok := f.tasks[id]; ok {
		return task, nil
	}
	return nil, ErrTaskNotFound
}

func (f *fakeTaskRepository) Update(_ context.Context, task *Task) error {
	f.tasks[task.ID] = task
	return nil
}

func (f *fakeTaskRepository) UpdateResumeState(_ context.Context, id int64, state *TaskResumeState) error {
	if task, ok := f.tasks[id]; ok {
		task.ResumeState = state
		task.Resumable = state != nil
	}
	return nil
}

func (f *fakeTaskRepository) ListByStatus(_ context.Context, statuses ...TaskStatus) ([]*Task, error) {
	out := make([]*Task, 0, len(f.tasks))
	for _, task := range f.tasks {
		for _, status := range statuses {
			if task.Status == status {
				out = append(out, task)
				break
			}
		}
	}
	return out, nil
}

func (f *fakeTaskRepository) Page(_ context.Context, _, _ int, _ ...TaskStatus) ([]*Task, int64, error) {
	return nil, 0, nil
}

// TestTransitionTaskNotifiesFinishedHandler 验证任务进入终态时回调 OnTaskFinished，
// 非终态（running / waiting_permission）不回调。
func TestTransitionTaskNotifiesFinishedHandler(t *testing.T) {
	// transitionTask 会广播事件，publish 里要用雪花 ID。
	_ = utils.InitSnowflake(1)

	svc := NewService(ServiceConfig{Tasks: newFakeTaskRepository()})
	var got []TaskStatus
	svc.SetTaskFinishedHandler(func(_ context.Context, task *Task) {
		got = append(got, task.Status)
	})

	ctx := context.Background()
	task := &Task{ID: 42, Status: TaskCreated}

	if err := svc.transitionTask(ctx, task, TaskRunning, ""); err != nil {
		t.Fatalf("transition to running: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("running should not notify finished handler, got %v", got)
	}

	if err := svc.transitionTask(ctx, task, TaskFailed, "boom"); err != nil {
		t.Fatalf("transition to failed: %v", err)
	}
	if len(got) != 1 || got[0] != TaskFailed {
		t.Fatalf("finished handler calls = %v, want [failed]", got)
	}
	if task.Error != "boom" {
		t.Fatalf("task error = %q, want boom", task.Error)
	}
}
