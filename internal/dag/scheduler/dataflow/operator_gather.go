package dataflow

import (
	"context"
	"strings"
	"sync"
)

// GatherOperator aggregates one field according to gather mode and emits once
// when all upstream channels are closed.
type GatherOperator struct {
	*BaseOperator
	analysisID   int64
	nodeID       string
	inputByChID  map[string]string
	requiredKeys []string
	buffers      map[string][]any
	gatherField  string
	gatherMode   string
	emitted      bool
	runtime      DataflowProcessRuntime
	mu           sync.Mutex
}

func newGatherOperator(
	analysisID int64,
	nodeID string,
	inputByChannelID map[string]string,
	outputChannels map[string]*DataflowChannel,
	gatherField string,
	gatherMode string,
	runtime DataflowProcessRuntime,
) *GatherOperator {
	common := newOperatorCommonConfig(analysisID, nodeID, inputByChannelID, runtime)
	o := &GatherOperator{
		analysisID:   common.analysisID,
		nodeID:       common.nodeID,
		inputByChID:  common.inputByChID,
		requiredKeys: common.requiredKeys,
		buffers:      make(map[string][]any, len(common.requiredKeys)),
		gatherField:  strings.TrimSpace(gatherField),
		gatherMode:   strings.ToLower(strings.TrimSpace(gatherMode)),
		runtime:      common.runtime,
	}
	o.BaseOperator = newBaseOperator(baseOperatorConfig{
		inputByChID:     o.inputByChID,
		outputChannels:  outputChannels,
		onData:          o.handleData,
		canFinish:       o.canFinish,
		beforeFinish:    o.beforeFinish,
		ignoreUnknownCh: true,
	})
	return o
}

func (o *GatherOperator) handleData(ctx context.Context, signal DataflowSignal) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	inputKey := o.inputByChID[signal.ChannelID]
	o.buffers[inputKey] = append(o.buffers[inputKey], signal.Value)
	return nil
}

func (o *GatherOperator) canFinish() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return !o.emitted
}

func (o *GatherOperator) beforeFinish(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.flushLocked(ctx)
}

func (o *GatherOperator) flushLocked(ctx context.Context) error {
	if o.emitted {
		return nil
	}
	inputs := make(map[string]any, len(o.requiredKeys))
	for _, key := range o.requiredKeys {
		queue := o.buffers[key]
		if key == o.gatherField && o.gatherMode == "list" {
			inputs[key] = append([]any(nil), queue...)
			continue
		}
		if len(queue) == 0 {
			return nil
		}
		inputs[key] = queue[0]
	}

	if o.runtime != nil {
		if err := o.runtime.SubmitProcessInstance(ctx, DataflowProcessRunRequest{
			AnalysisID: o.analysisID,
			NodeID:     o.nodeID,
			Inputs:     inputs,
			Reason:     "gather-list-ready",
		}); err != nil {
			return err
		}
	}
	o.emitted = true
	return nil
}
