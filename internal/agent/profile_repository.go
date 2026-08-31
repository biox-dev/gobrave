package agent

import (
	"context"
	"sort"
	"sync"
)

// ProfileRepository 持久化用户自定义 AgentProfile。
//
// 与 TaskRepository / MemoryRepository 一致：内置 Profile 由代码定义（不落库），
// 仅用户自定义 Profile 通过该接口持久化。
type ProfileRepository interface {
	Create(ctx context.Context, profile *Profile) error
	Get(ctx context.Context, id string) (*Profile, error)
	Update(ctx context.Context, profile *Profile) error
	Delete(ctx context.Context, id string) error
	// ListByUser 返回某用户全部自定义 Profile（按名称升序）。
	ListByUser(ctx context.Context, userID string) ([]*Profile, error)
	// GetByName 按名称返回某用户的自定义 Profile。
	GetByName(ctx context.Context, userID, name string) (*Profile, error)
	// GetDefault 返回某用户的默认自定义 Profile；不存在时返回 (nil, nil)。
	GetDefault(ctx context.Context, userID string) (*Profile, error)
	// ClearDefault 清除某用户除 exceptID 外的默认标记。
	ClearDefault(ctx context.Context, userID, exceptID string) error
}

// NewMemoryProfileRepository 创建 Profile 的内存实现。
func NewMemoryProfileRepository() ProfileRepository {
	return &memoryProfileRepository{profiles: make(map[string]*Profile)}
}

type memoryProfileRepository struct {
	mu       sync.RWMutex
	profiles map[string]*Profile
}

func cloneProfile(p *Profile) *Profile {
	if p == nil {
		return nil
	}
	d := *p
	d.Skills = append([]string(nil), p.Skills...)
	return &d
}

func (r *memoryProfileRepository) Create(_ context.Context, p *Profile) error {
	if p == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profiles[p.ID] = cloneProfile(p)
	return nil
}

func (r *memoryProfileRepository) Get(_ context.Context, id string) (*Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.profiles[id]
	if !ok {
		return nil, ErrProfileNotFound
	}
	return cloneProfile(p), nil
}

func (r *memoryProfileRepository) Update(_ context.Context, p *Profile) error {
	if p == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profiles[p.ID] = cloneProfile(p)
	return nil
}

func (r *memoryProfileRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.profiles, id)
	return nil
}

func (r *memoryProfileRepository) ListByUser(_ context.Context, userID string) ([]*Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Profile, 0)
	for _, p := range r.profiles {
		if p.UserID == userID {
			out = append(out, cloneProfile(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *memoryProfileRepository) GetByName(_ context.Context, userID, name string) (*Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.profiles {
		if p.UserID == userID && p.Name == name {
			return cloneProfile(p), nil
		}
	}
	return nil, ErrProfileNotFound
}

func (r *memoryProfileRepository) GetDefault(_ context.Context, userID string) (*Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.profiles {
		if p.UserID == userID && p.IsDefault {
			return cloneProfile(p), nil
		}
	}
	return nil, nil
}

func (r *memoryProfileRepository) ClearDefault(_ context.Context, userID, exceptID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.profiles {
		if p.UserID == userID && p.ID != exceptID && p.IsDefault {
			p.IsDefault = false
		}
	}
	return nil
}
