package orchestratorv2

import "time"

// Scheduler tunables. Every value has a safe default so the orchestrator can be
// constructed with Dependencies only.
const (
	// defaultWorkers is the number of concurrent node dispatchers.
	defaultWorkers = 1
	// defaultReadyQueueSize bounds how many claimed nodes wait for a worker.
	defaultReadyQueueSize = 64
	// defaultLeaseTTL is the staleness window of the DB "running" lease.
	// A run whose updated_at is older than now-TTL may be taken over.
	defaultLeaseTTL = 90 * time.Second
	// defaultHeartbeatInterval is the lease renewal cadence while a run is active.
	defaultHeartbeatInterval = 15 * time.Second
	// defaultStopCheckInterval probes the persisted stop flags.
	defaultStopCheckInterval = 1 * time.Second
	// defaultWatchdogInterval re-runs reconciliation as a safety net.
	defaultWatchdogInterval = 5 * time.Second
	// defaultEventBuffer is the per-run runtime event buffer size.
	defaultEventBuffer = 256
)

// Options holds the resolved scheduler tunables.
type Options struct {
	// Workers is the number of concurrent node executions.
	Workers int
	// ReadyQueueSize bounds claimed-but-not-yet-running nodes.
	ReadyQueueSize int
	// LeaseTTL is the staleness window of the DB running lease.
	LeaseTTL time.Duration
	// HeartbeatInterval is the lease renewal cadence.
	HeartbeatInterval time.Duration
	// StopCheckInterval is the persisted stop-flag probe cadence.
	StopCheckInterval time.Duration
	// WatchdogInterval is the reconciliation safety-net cadence.
	WatchdogInterval time.Duration
	// EventBuffer is the runtime event buffer size per run.
	EventBuffer int
	// CachePolicies resolves a cache strategy per analysis cache type.
	CachePolicies *CachePolicyRegistry
	// Retry decides whether a failed node is re-queued in place.
	Retry RetryPolicy
}

// Option mutates Options during construction.
type Option func(*Options)

// defaultOptions returns the baseline scheduler configuration.
func defaultOptions() Options {
	return Options{
		Workers:           defaultWorkers,
		ReadyQueueSize:    defaultReadyQueueSize,
		LeaseTTL:          defaultLeaseTTL,
		HeartbeatInterval: defaultHeartbeatInterval,
		StopCheckInterval: defaultStopCheckInterval,
		WatchdogInterval:  defaultWatchdogInterval,
		EventBuffer:       defaultEventBuffer,
		CachePolicies:     NewCachePolicyRegistry(),
		Retry:             NoRetryPolicy{},
	}
}

// apply runs every Option in order.
func (o *Options) apply(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
}

// normalise repairs out-of-range values coming from user supplied options.
func (o Options) normalise() Options {
	if o.Workers <= 0 {
		o.Workers = defaultWorkers
	}
	if o.ReadyQueueSize <= 0 {
		o.ReadyQueueSize = defaultReadyQueueSize
	}
	if o.LeaseTTL <= 0 {
		o.LeaseTTL = defaultLeaseTTL
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = defaultHeartbeatInterval
	}
	if o.StopCheckInterval <= 0 {
		o.StopCheckInterval = defaultStopCheckInterval
	}
	if o.WatchdogInterval <= 0 {
		o.WatchdogInterval = defaultWatchdogInterval
	}
	if o.EventBuffer <= 0 {
		o.EventBuffer = defaultEventBuffer
	}
	if o.CachePolicies == nil {
		o.CachePolicies = NewCachePolicyRegistry()
	}
	if o.Retry == nil {
		o.Retry = NoRetryPolicy{}
	}
	return o
}

// WithWorkers overrides the number of concurrent node executions.
func WithWorkers(workers int) Option {
	return func(o *Options) { o.Workers = workers }
}

// WithReadyQueueSize overrides the claimed-node queue size.
func WithReadyQueueSize(size int) Option {
	return func(o *Options) { o.ReadyQueueSize = size }
}

// WithLeaseTTL overrides the DB running lease staleness window.
func WithLeaseTTL(ttl time.Duration) Option {
	return func(o *Options) { o.LeaseTTL = ttl }
}

// WithHeartbeatInterval overrides the lease renewal cadence.
func WithHeartbeatInterval(interval time.Duration) Option {
	return func(o *Options) { o.HeartbeatInterval = interval }
}

// WithStopCheckInterval overrides the stop-flag probe cadence.
func WithStopCheckInterval(interval time.Duration) Option {
	return func(o *Options) { o.StopCheckInterval = interval }
}

// WithWatchdogInterval overrides the reconciliation safety-net cadence.
func WithWatchdogInterval(interval time.Duration) Option {
	return func(o *Options) { o.WatchdogInterval = interval }
}

// WithEventBuffer overrides the per-run runtime event buffer size.
func WithEventBuffer(size int) Option {
	return func(o *Options) { o.EventBuffer = size }
}

// WithCachePolicies replaces the whole cache policy registry.
func WithCachePolicies(registry *CachePolicyRegistry) Option {
	return func(o *Options) { o.CachePolicies = registry }
}

// WithRetryPolicy replaces the retry strategy.
func WithRetryPolicy(policy RetryPolicy) Option {
	return func(o *Options) { o.Retry = policy }
}
