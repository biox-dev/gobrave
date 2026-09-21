package trace

import "sync"

// Registry keeps at most one Tracer per run key.
//
// A scheduler stores its per-run tracer here instead of threading a *Tracer
// parameter through every method of a run: the key (the analysis id) already
// identifies the run, so instrumenting a new step stays a one-line change at the
// call site rather than a signature change through the call chain.
//
// A nil *Registry is valid and every method is a no-op, so an orchestrator
// assembled without tracing keeps working.
type Registry[K comparable] struct {
	mu      sync.Mutex
	tracers map[K]*Tracer
}

// NewRegistry creates an empty, ready-to-use registry.
func NewRegistry[K comparable]() *Registry[K] {
	return &Registry[K]{tracers: make(map[K]*Tracer)}
}

// Open starts a fresh trace for key and returns it.
//
// A previous tracer for the same key is closed, so a restarted run can never keep
// an open handle on (or append to) the trace of an earlier attempt.
func (r *Registry[K]) Open(key K, path string, schema *Schema) (*Tracer, error) {
	tracer, err := Open(path, schema)
	if err != nil {
		return nil, err
	}
	if r == nil {
		// No registry to remember the tracer in: close it so the caller does not
		// leak the file handle, and report an untraced run.
		_ = tracer.Close()
		return nil, nil
	}

	r.mu.Lock()
	previous := r.tracers[key]
	r.tracers[key] = tracer
	r.mu.Unlock()

	// Closing outside the lock keeps a slow file close from blocking other runs.
	_ = previous.Close()
	return tracer, nil
}

// Get returns the tracer of key, or nil when the run is not traced.
func (r *Registry[K]) Get(key K) *Tracer {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tracers[key]
}

// Close closes and forgets the tracer of key. It is safe to call for a key that
// was never opened, which is what lets a failure path clean up unconditionally.
func (r *Registry[K]) Close(key K) {
	if r == nil {
		return
	}
	r.mu.Lock()
	tracer, ok := r.tracers[key]
	if ok {
		delete(r.tracers, key)
	}
	r.mu.Unlock()

	if ok {
		_ = tracer.Close()
	}
}
