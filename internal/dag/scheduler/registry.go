package scheduler

import (
	"sort"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// Registry is the process-wide lookup table of DAG schedulers.
//
// Every orchestrator implementation exposes a stable Name() which is persisted in
// analysis.scheduler_mode. Submitting a run, stopping it and crash recovery all
// resolve the owning scheduler through this single table, so the three code paths
// can never disagree about who advances an analysis.
//
// The pattern mirrors internal/container_runtime/factory.go: implementations are
// registered once at startup and looked up by name afterwards. Adding a scheduler
// is a register call here plus a NormalizeSchedulerMode entry in internal/types.
type Registry struct {
	orchestrators map[string]interfaces.DagOrchestrator
	order         []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{orchestrators: map[string]interfaces.DagOrchestrator{}}
}

// Register stores an orchestrator under the name it reports.
func (r *Registry) Register(o interfaces.DagOrchestrator) {
	if o == nil {
		return
	}
	r.RegisterName(o.Name(), o)
}

// RegisterName stores an orchestrator under an explicit name. The name is
// normalized so both the canonical value ("dynamic") and a legacy value
// ("dynamic_v2") resolve to the same entry.
func (r *Registry) RegisterName(name string, o interfaces.DagOrchestrator) {
	if r == nil || o == nil {
		return
	}
	key := types.NormalizeSchedulerMode(name)
	if _, exists := r.orchestrators[key]; !exists {
		r.order = append(r.order, key)
	}
	r.orchestrators[key] = o
}

// Get returns the orchestrator registered for name, or nil when none matches.
func (r *Registry) Get(name string) interfaces.DagOrchestrator {
	if r == nil {
		return nil
	}
	return r.orchestrators[types.NormalizeSchedulerMode(name)]
}

// Resolve returns the orchestrator that owns an analysis whose persisted
// scheduler_mode is mode. Empty and unknown values fall back to the default
// scheduler through types.NormalizeSchedulerMode.
func (r *Registry) Resolve(mode string) interfaces.DagOrchestrator {
	return r.Get(mode)
}

// Has reports whether an orchestrator is registered for name.
func (r *Registry) Has(name string) bool {
	return r.Get(name) != nil
}

// List returns the registered orchestrators in registration order.
func (r *Registry) List() []interfaces.DagOrchestrator {
	if r == nil {
		return nil
	}
	items := make([]interfaces.DagOrchestrator, 0, len(r.order))
	for _, name := range r.order {
		if o := r.orchestrators[name]; o != nil {
			items = append(items, o)
		}
	}
	return items
}

// Names returns the registered scheduler names in registration order.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := append([]string(nil), r.order...)
	sort.Strings(names)
	return names
}
