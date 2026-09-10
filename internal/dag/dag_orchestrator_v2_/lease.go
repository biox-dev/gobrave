package orchestratorv2

import (
	"context"
	"time"

	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// LeaseKeeper guards a DAG run across scheduler instances.
//
// The lease is stored in the analysis row itself (analysis.updated_at), which is
// what the Python implementation exposed as running_dag_registry: a run whose
// timestamp stopped moving is stale and may be taken over.
type LeaseKeeper interface {
	// Acquire tries to take the lease. It returns false when another live
	// scheduler already owns it.
	Acquire(ctx context.Context, analysisID int64) (bool, error)
	// Renew extends the lease so other instances see the run as alive.
	Renew(ctx context.Context, analysisID int64) error
	// KeepAlive renews the lease periodically until stop is closed or ctx ends.
	KeepAlive(ctx context.Context, analysisID int64, stop <-chan struct{})
}

// RepositoryLease is the LeaseKeeper backed by the analysis repository.
type RepositoryLease struct {
	repo     interfaces.AnalysisRepository
	ttl      time.Duration
	interval time.Duration
}

// NewRepositoryLease builds a repository backed lease.
func NewRepositoryLease(repo interfaces.AnalysisRepository, ttl, interval time.Duration) *RepositoryLease {
	if ttl <= 0 {
		ttl = defaultLeaseTTL
	}
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	return &RepositoryLease{repo: repo, ttl: ttl, interval: interval}
}

// Acquire implements LeaseKeeper.
func (l *RepositoryLease) Acquire(ctx context.Context, analysisID int64) (bool, error) {
	now := time.Now().UTC()
	return l.repo.TryMarkAnalysisRunning(ctx, analysisID, now, now.Add(-l.ttl))
}

// Renew implements LeaseKeeper.
func (l *RepositoryLease) Renew(ctx context.Context, analysisID int64) error {
	return l.repo.UpdateAnalysisByID(ctx, analysisID, map[string]any{
		"updated_at": time.Now().UTC(),
	})
}

// KeepAlive implements LeaseKeeper.
func (l *RepositoryLease) KeepAlive(ctx context.Context, analysisID int64, stop <-chan struct{}) {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			// Best effort: a missed heartbeat only risks a takeover after the TTL.
			_ = l.Renew(context.Background(), analysisID)
		}
	}
}
