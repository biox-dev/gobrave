package containerruntime

import (
	"strings"
	"sync"
)

// MonitoringRegistry abstracts runtime monitoring membership operations.
// Implementations can be in-memory, Redis-backed, etc.
type MonitoringRegistry interface {
	Mark(runtimeID string)
	Unmark(runtimeID string)
	IsMonitoring(runtimeID string) bool
	MarkIfNotMonitoring(runtimeID string) bool
	Snapshot() map[string]int
}

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

var globalMonitoringRegistry = struct {
	mu  sync.RWMutex
	reg MonitoringRegistry
}{
	reg: NewInMemoryMonitoringRegistry(),
}

// SetMonitoringRegistry replaces the process-global monitoring registry.
// Passing nil is ignored.
func SetMonitoringRegistry(reg MonitoringRegistry) {
	if reg == nil {
		return
	}

	globalMonitoringRegistry.mu.Lock()
	defer globalMonitoringRegistry.mu.Unlock()
	globalMonitoringRegistry.reg = reg
}

func getMonitoringRegistry() MonitoringRegistry {
	globalMonitoringRegistry.mu.RLock()
	defer globalMonitoringRegistry.mu.RUnlock()
	if globalMonitoringRegistry.reg == nil {
		return NewInMemoryMonitoringRegistry()
	}
	return globalMonitoringRegistry.reg
}

// MarkRuntimeMonitoring marks runtimeID as actively monitored.
// It is reference-counted so multiple monitor goroutines for the same runtimeID
// can coexist safely.
func MarkRuntimeMonitoring(runtimeID string) {
	getMonitoringRegistry().Mark(runtimeID)
}

// UnmarkRuntimeMonitoring releases one monitoring reference for runtimeID.
func UnmarkRuntimeMonitoring(runtimeID string) {
	getMonitoringRegistry().Unmark(runtimeID)
}

// IsRuntimeMonitoring reports whether runtimeID is currently monitored.
func IsRuntimeMonitoring(runtimeID string) bool {
	return getMonitoringRegistry().IsMonitoring(runtimeID)
}

// MarkIfNotMonitoring atomically marks runtimeID as monitored if it is not already.
// Returns true if the runtimeID was newly marked.
func MarkIfNotMonitoring(runtimeID string) bool {
	return getMonitoringRegistry().MarkIfNotMonitoring(runtimeID)
}

// RuntimeMonitoringSnapshot returns a copy of the process-global monitoring table.
// Key is runtimeID and value is monitoring reference count.
func RuntimeMonitoringSnapshot() map[string]int {
	return getMonitoringRegistry().Snapshot()
}
