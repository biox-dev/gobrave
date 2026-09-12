package dataflow

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/logger"
)

// DataflowChannel models a Nextflow-like channel lifecycle:
// open -> emit* -> close.
type DataflowChannel struct {
	id          string
	state       dataflowChannelState
	subscribers []DataflowOperator
	mu          sync.RWMutex
}

func newDataflowChannel(id string) *DataflowChannel {
	return &DataflowChannel{
		id:    strings.TrimSpace(id),
		state: dataflowChannelStateOpen,
	}
}

func (ch *DataflowChannel) ID() string {
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	return ch.id
}

func (ch *DataflowChannel) Subscribe(op DataflowOperator) {
	if op == nil {
		return
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.subscribers = append(ch.subscribers, op)
}

func (ch *DataflowChannel) Emit(ctx context.Context, value any) error {
	ch.mu.RLock()
	if ch.state != dataflowChannelStateOpen {
		ch.mu.RUnlock()
		logger.Errorf(context.Background(), "[DataflowDagOrchestratorV3] emit to closed channel, channel_id=%s", ch.id)
		return fmt.Errorf("channel %s is not open", ch.id)
	}
	subs := append([]DataflowOperator(nil), ch.subscribers...)
	channelID := ch.id
	ch.mu.RUnlock()

	for _, op := range subs {
		if err := op.Notify(ctx, DataflowSignal{ChannelID: channelID, Value: value, Closed: false}); err != nil {
			return err
		}
	}
	return nil
}

func (ch *DataflowChannel) Close(ctx context.Context) error {
	ch.mu.Lock()
	if ch.state == dataflowChannelStateClosed {
		ch.mu.Unlock()
		return nil
	}
	ch.state = dataflowChannelStateClosed
	subs := append([]DataflowOperator(nil), ch.subscribers...)
	channelID := ch.id
	ch.mu.Unlock()

	for _, op := range subs {
		if err := op.Notify(ctx, DataflowSignal{ChannelID: channelID, Closed: true}); err != nil {
			return err
		}
	}
	return nil
}

func (ch *DataflowChannel) IsClosed() bool {
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	return ch.state == dataflowChannelStateClosed
}
