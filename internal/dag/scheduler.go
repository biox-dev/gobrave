package dag

import (
	"context"
	"time"

	"github.com/biox-dev/gobrave/internal/event"
)

type SchedulerConfig struct {
	MaxSteps       int
	MaxConcurrency int
	QueueSize      int
	PollInterval   time.Duration
	Timeout        time.Duration
}

type SchedulerResult struct {
	AnalysisID          int64            `json:"analysis_id"`
	SubmittedCount      int              `json:"submitted_count"`
	FailedToSubmitCount int              `json:"failed_to_submit_count"`
	TimedOut            bool             `json:"timed_out"`
	Snapshot            *RuntimeSnapshot `json:"snapshot"`
}

type DagScheduler struct {
	analysisID int64
	runtime    *RuntimeEngine
	dispatcher *NodeDispatcher
	bus        event.Bus
	cfg        SchedulerConfig
}

func NewDagScheduler(analysisID int64, runtime *RuntimeEngine, dispatcher *NodeDispatcher, bus event.Bus, cfg SchedulerConfig) *DagScheduler {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 10000
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 1
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	return &DagScheduler{
		analysisID: analysisID,
		runtime:    runtime,
		dispatcher: dispatcher,
		bus:        bus,
		cfg:        cfg,
	}
}

func (s *DagScheduler) Run(ctx context.Context) (*SchedulerResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if s.cfg.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
		defer cancel()
	}

	pool := NewWorkerPool(s.dispatcher, s.cfg.MaxConcurrency, s.cfg.QueueSize)
	pool.Start(ctx)
	defer pool.Stop()

	s.publish(RuntimeEvent{Name: EventDagStarted, AnalysisID: s.analysisID, OccurredAt: time.Now().UTC()})

	submitted := 0
	failedToSubmit := 0
	timedOut := false
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	probeInterval := s.cfg.PollInterval
	maxProbeInterval := s.cfg.PollInterval * 8
	if maxProbeInterval < s.cfg.PollInterval {
		maxProbeInterval = s.cfg.PollInterval
	}
	nextProbeAt := time.Now()

	for submitted < s.cfg.MaxSteps {
		queueLen := pool.QueueLen()
		now := time.Now()
		if queueLen < s.cfg.QueueSize && !now.Before(nextProbeAt) {
			if err := s.runtime.RefreshReadyStatus(ctx, s.analysisID); err != nil {
				return nil, err
			}

			slots := s.cfg.QueueSize - queueLen
			remaining := s.cfg.MaxSteps - submitted
			if slots > remaining {
				slots = remaining
			}
			hasProgress := false

			for i := 0; i < slots; i++ {
				node, err := s.runtime.ClaimNextReadyNode(ctx, s.analysisID)
				if err != nil {
					failedToSubmit++
					break
				}
				if node == nil {
					break
				}
				if ok := pool.Enqueue(node.ID); !ok {
					failedToSubmit++
					break
				}
				submitted++
				hasProgress = true
				s.publish(RuntimeEvent{
					Name:           EventNodeSubmitted,
					AnalysisID:     s.analysisID,
					AnalysisNodeID: node.ID,
					NodeID:         node.NodeID,
					OccurredAt:     time.Now().UTC(),
				})
			}

			if hasProgress {
				probeInterval = s.cfg.PollInterval
			} else if probeInterval < maxProbeInterval {
				probeInterval *= 2
				if probeInterval > maxProbeInterval {
					probeInterval = maxProbeInterval
				}
			}
			nextProbeAt = now.Add(probeInterval)
		} else if queueLen >= s.cfg.QueueSize {
			// Queue saturation means workers are busy; probe again as soon as capacity is available.
			probeInterval = s.cfg.PollInterval
			nextProbeAt = now
		}

		snapshot, err := s.runtime.GetSnapshot(ctx, s.analysisID)
		if err != nil {
			return nil, err
		}

		if snapshot.IsFinished && pool.QueueLen() == 0 {
			s.publish(RuntimeEvent{Name: EventDagCompleted, AnalysisID: s.analysisID, OccurredAt: time.Now().UTC()})
			return &SchedulerResult{
				AnalysisID:          s.analysisID,
				SubmittedCount:      submitted,
				FailedToSubmitCount: failedToSubmit,
				TimedOut:            false,
				Snapshot:            snapshot,
			}, nil
		}

		select {
		case <-ctx.Done():
			timedOut = true
			finalSnapshot, _ := s.runtime.GetSnapshot(context.Background(), s.analysisID)
			s.publish(RuntimeEvent{Name: EventDagFailed, AnalysisID: s.analysisID, OccurredAt: time.Now().UTC(), Payload: map[string]any{"reason": ctx.Err().Error()}})
			return &SchedulerResult{
				AnalysisID:          s.analysisID,
				SubmittedCount:      submitted,
				FailedToSubmitCount: failedToSubmit,
				TimedOut:            timedOut,
				Snapshot:            finalSnapshot,
			}, nil
		case <-ticker.C:
		}
	}

	snapshot, _ := s.runtime.GetSnapshot(context.Background(), s.analysisID)
	s.publish(RuntimeEvent{Name: EventDagCompleted, AnalysisID: s.analysisID, OccurredAt: time.Now().UTC(), Payload: map[string]any{"max_steps_reached": true}})
	return &SchedulerResult{
		AnalysisID:          s.analysisID,
		SubmittedCount:      submitted,
		FailedToSubmitCount: failedToSubmit,
		TimedOut:            false,
		Snapshot:            snapshot,
	}, nil
}

func (s *DagScheduler) publish(evt RuntimeEvent) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(evt)
}
