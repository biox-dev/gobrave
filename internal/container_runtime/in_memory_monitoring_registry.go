package containerruntime

import (
	"strings"
	"sync"
)

type inMemoryMonitoringRegistry struct {
	mu     sync.RWMutex
	counts map[string]int
}

func NewInMemoryMonitoringRegistry() MonitoringRegistry {
	return &inMemoryMonitoringRegistry{counts: map[string]int{}}
}

func (r *inMemoryMonitoringRegistry) Mark(runtimeID string) {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[runtimeID] = r.counts[runtimeID] + 1
}

func (r *inMemoryMonitoringRegistry) Unmark(runtimeID string) {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	count, ok := r.counts[runtimeID]
	if !ok {
		return
	}
	if count <= 1 {
		delete(r.counts, runtimeID)
		return
	}
	r.counts[runtimeID] = count - 1
}

func (r *inMemoryMonitoringRegistry) IsMonitoring(runtimeID string) bool {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.counts[runtimeID] > 0
}

func (r *inMemoryMonitoringRegistry) MarkIfNotMonitoring(runtimeID string) bool {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts[runtimeID] > 0 {
		return false
	}
	r.counts[runtimeID] = 1
	return true
}

func (r *inMemoryMonitoringRegistry) Snapshot() map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]int, len(r.counts))
	for runtimeID, count := range r.counts {
		out[runtimeID] = count
	}
	return out
}
