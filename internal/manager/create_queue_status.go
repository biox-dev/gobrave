package manager

type CreateQueueStatus struct {
	ActiveCount    int64
	PendingCount   int64
	MaxConcurrency int
	MaxPending     int
}
