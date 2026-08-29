package agent

import (
	"context"
	"sort"
	"sync"
)

// 本文件定义持久化抽象（Repository 接口）及其内存实现。
//
// 设计原则（见 design.md）：数据库才是状态源，EventBus / WS 只是通知层。
// 因此 Repository 接口被设计为可替换的——当前提供内存实现以保证链路可跑通，
// 后续可直接替换为 gorm / sqlc 等基于 DB 的实现，而无需改动上层编排逻辑。

// TaskRepository 持久化 Agent 任务。
type TaskRepository interface {
	Create(ctx context.Context, task *Task) error
	Get(ctx context.Context, id string) (*Task, error)
	Update(ctx context.Context, task *Task) error
	ListByStatus(ctx context.Context, statuses ...TaskStatus) ([]*Task, error)
	// Page 分页查询任务（按 CreatedAt 升序）；statuses 为空表示全部状态。
	Page(ctx context.Context, offset, limit int, statuses ...TaskStatus) ([]*Task, int64, error)
}

// PermissionRepository 持久化权限请求。
type PermissionRepository interface {
	Create(ctx context.Context, permission *PermissionRequest) error
	Get(ctx context.Context, id string) (*PermissionRequest, error)
	Update(ctx context.Context, permission *PermissionRequest) error
	ListPendingByTask(ctx context.Context, taskID string) ([]*PermissionRequest, error)
	// Page 分页查询权限请求（按 CreatedAt 升序）；taskID 为空表示全部任务，statuses 为空表示全部状态。
	Page(ctx context.Context, offset, limit int, taskID string, statuses ...PermissionStatus) ([]*PermissionRequest, int64, error)
}

// EventRepository 持久化任务事件（带 per-task 单调递增 sequence）。
type EventRepository interface {
	// Append 为事件分配 sequence 并追加到任务事件流。
	Append(ctx context.Context, event *AgentEvent) error
	// ListByTask 返回任务中 sequence 大于 after 的事件（升序）。
	ListByTask(ctx context.Context, taskID string, after int64) ([]*AgentEvent, error)
	// Page 分页查询事件（按 CreatedAt 升序）；taskID 为空表示全部任务。
	Page(ctx context.Context, taskID string, offset, limit int) ([]*AgentEvent, int64, error)
}

// NewMemoryTaskRepository 创建任务的内存实现。
func NewMemoryTaskRepository() TaskRepository {
	return &memoryTaskRepository{tasks: make(map[string]*Task)}
}

// NewMemoryPermissionRepository 创建权限请求的内存实现。
func NewMemoryPermissionRepository() PermissionRepository {
	return &memoryPermissionRepository{perms: make(map[string]*PermissionRequest)}
}

// NewMemoryEventRepository 创建事件流的内存实现。
func NewMemoryEventRepository() EventRepository {
	return &memoryEventRepository{events: make(map[string][]*AgentEvent)}
}

// ---- Task ----

type memoryTaskRepository struct {
	mu    sync.RWMutex
	tasks map[string]*Task
}

func (r *memoryTaskRepository) Create(_ context.Context, task *Task) error {
	if task == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[task.ID] = cloneTask(task)
	return nil
}

func (r *memoryTaskRepository) Get(_ context.Context, id string) (*Task, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[id]
	if !ok {
		return nil, ErrTaskNotFound
	}
	return cloneTask(t), nil
}

func (r *memoryTaskRepository) Update(_ context.Context, task *Task) error {
	if task == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[task.ID] = cloneTask(task)
	return nil
}

func (r *memoryTaskRepository) ListByStatus(_ context.Context, statuses ...TaskStatus) ([]*Task, error) {
	want := make(map[TaskStatus]bool, len(statuses))
	for _, s := range statuses {
		want[s] = true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Task, 0)
	for _, t := range r.tasks {
		if want[t.Status] {
			out = append(out, cloneTask(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *memoryTaskRepository) Page(_ context.Context, offset, limit int, statuses ...TaskStatus) ([]*Task, int64, error) {
	want := make(map[TaskStatus]bool, len(statuses))
	for _, s := range statuses {
		want[s] = true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]*Task, 0)
	for _, t := range r.tasks {
		if len(want) == 0 || want[t.Status] {
			all = append(all, t)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	total := int64(len(all))
	out := make([]*Task, 0, limit)
	for _, t := range sliceRange(all, offset, limit) {
		out = append(out, cloneTask(t))
	}
	return out, total, nil
}

// ---- Permission ----

type memoryPermissionRepository struct {
	mu    sync.RWMutex
	perms map[string]*PermissionRequest
}

func (r *memoryPermissionRepository) Create(_ context.Context, p *PermissionRequest) error {
	if p == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.perms[p.ID] = clonePermission(p)
	return nil
}

func (r *memoryPermissionRepository) Get(_ context.Context, id string) (*PermissionRequest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.perms[id]
	if !ok {
		return nil, ErrPermissionNotFound
	}
	return clonePermission(p), nil
}

func (r *memoryPermissionRepository) Update(_ context.Context, p *PermissionRequest) error {
	if p == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.perms[p.ID] = clonePermission(p)
	return nil
}

func (r *memoryPermissionRepository) ListPendingByTask(_ context.Context, taskID string) ([]*PermissionRequest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*PermissionRequest, 0)
	for _, p := range r.perms {
		if p.TaskID == taskID && p.Status == PermissionPending {
			out = append(out, clonePermission(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *memoryPermissionRepository) Page(_ context.Context, offset, limit int, taskID string, statuses ...PermissionStatus) ([]*PermissionRequest, int64, error) {
	want := make(map[PermissionStatus]bool, len(statuses))
	for _, s := range statuses {
		want[s] = true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]*PermissionRequest, 0)
	for _, p := range r.perms {
		if taskID != "" && p.TaskID != taskID {
			continue
		}
		if len(want) > 0 && !want[p.Status] {
			continue
		}
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	total := int64(len(all))
	out := make([]*PermissionRequest, 0, limit)
	for _, p := range sliceRange(all, offset, limit) {
		out = append(out, clonePermission(p))
	}
	return out, total, nil
}

// ---- Event ----

type memoryEventRepository struct {
	mu     sync.RWMutex
	events map[string][]*AgentEvent // taskID → 事件流（升序）
}

func (r *memoryEventRepository) Append(_ context.Context, event *AgentEvent) error {
	if event == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stream := r.events[event.TaskID]
	if len(stream) > 0 {
		event.Sequence = stream[len(stream)-1].Sequence + 1
	} else {
		event.Sequence = 1
	}
	r.events[event.TaskID] = append(stream, event)
	return nil
}

func (r *memoryEventRepository) ListByTask(_ context.Context, taskID string, after int64) ([]*AgentEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	stream := r.events[taskID]
	out := make([]*AgentEvent, 0)
	for _, e := range stream {
		if e.Sequence > after {
			out = append(out, e)
		}
	}
	return out, nil
}

func (r *memoryEventRepository) Page(_ context.Context, taskID string, offset, limit int) ([]*AgentEvent, int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]*AgentEvent, 0)
	if taskID != "" {
		all = append(all, r.events[taskID]...)
	} else {
		for _, stream := range r.events {
			all = append(all, stream...)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].Sequence < all[j].Sequence
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	total := int64(len(all))
	out := make([]*AgentEvent, 0, limit)
	out = append(out, sliceRange(all, offset, limit)...)
	return out, total, nil
}

// sliceRange 对已排序的切片按 offset/limit 截取（越界安全）。
func sliceRange[T any](items []T, offset, limit int) []T {
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	if offset >= len(items) {
		return items[:0:0]
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// ---- clone helpers（内存实现返回副本，避免并发读写共享结构） ----

func cloneTask(t *Task) *Task {
	if t == nil {
		return nil
	}
	c := *t
	c.Request = cloneRequest(t.Request)
	if t.StartedAt != nil {
		v := *t.StartedAt
		c.StartedAt = &v
	}
	if t.FinishedAt != nil {
		v := *t.FinishedAt
		c.FinishedAt = &v
	}
	return &c
}

func clonePermission(p *PermissionRequest) *PermissionRequest {
	if p == nil {
		return nil
	}
	c := *p
	c.Operation = cloneOperation(p.Operation)
	if p.ResolvedAt != nil {
		v := *p.ResolvedAt
		c.ResolvedAt = &v
	}
	if p.ResolvedBy != nil {
		v := *p.ResolvedBy
		c.ResolvedBy = &v
	}
	return &c
}

func cloneRequest(r Request) Request {
	r.Messages = append([]Message(nil), r.Messages...)
	if r.Env != nil {
		env := make(map[string]string, len(r.Env))
		for k, v := range r.Env {
			env[k] = v
		}
		r.Env = env
	}
	return r
}

func cloneOperation(o Operation) Operation {
	if o.Metadata != nil {
		md := make(map[string]any, len(o.Metadata))
		for k, v := range o.Metadata {
			md[k] = v
		}
		o.Metadata = md
	}
	return o
}
