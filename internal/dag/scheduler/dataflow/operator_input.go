package dataflow

import (
	"context"
	"sync"
)

// InputOperator is the default operator for regular inputs.
// It collects values from one or more upstream channels and materializes
// a process instance when all required inputs have at least one value.
//
// This is the V3 equivalent of:
// - cache.Add(v)
// - if Ready() { runtime.SubmitTask(...) }
type InputOperator struct {
	*BaseOperator
	analysisID   int64
	nodeID       string
	inputByChID  map[string]string
	requiredKeys []string
	buffers      map[string][]any
	receivedCnt  map[string]int
	runtime      DataflowProcessRuntime
	mu           sync.Mutex
}

func newInputOperator(
	analysisID int64,
	nodeID string,
	inputByChannelID map[string]string,
	outputChannels map[string]*DataflowChannel,
	runtime DataflowProcessRuntime,
) *InputOperator {
	common := newOperatorCommonConfig(analysisID, nodeID, inputByChannelID, runtime)
	o := &InputOperator{
		analysisID:   common.analysisID,
		nodeID:       common.nodeID,
		inputByChID:  common.inputByChID,
		requiredKeys: common.requiredKeys,
		buffers:      make(map[string][]any, len(common.requiredKeys)),
		receivedCnt:  make(map[string]int, len(common.requiredKeys)),
		runtime:      common.runtime,
	}
	o.BaseOperator = newBaseOperator(baseOperatorConfig{
		inputByChID:     o.inputByChID,
		outputChannels:  outputChannels,
		onData:          o.handleData,
		ignoreUnknownCh: true,
	})
	return o
}

func (o *InputOperator) handleData(ctx context.Context, signal DataflowSignal) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	inputKey := o.inputByChID[signal.ChannelID]

	o.buffers[inputKey] = append(o.buffers[inputKey], signal.Value)
	o.receivedCnt[inputKey]++
	for o.ready() {
		inputs := make(map[string]any, len(o.requiredKeys))
		consumeByKey := make(map[string]bool, len(o.requiredKeys))
		consumedAny := false
		for _, key := range o.requiredKeys {
			queue := o.buffers[key]
			value, consume := o.pickInputValueLocked(key, queue)
			inputs[key] = value
			consumeByKey[key] = consume
			if consume {
				consumedAny = true
			}
		}
		if !consumedAny {
			for _, key := range o.requiredKeys {
				if len(o.buffers[key]) == 0 {
					continue
				}
				consumeByKey[key] = true
				consumedAny = true
			}
		}
		for _, key := range o.requiredKeys {
			if !consumeByKey[key] {
				continue
			}
			queue := o.buffers[key]
			if len(queue) == 0 {
				continue
			}
			o.buffers[key] = queue[1:]
		}
		if o.runtime != nil {
			if err := o.runtime.SubmitProcessInstance(ctx, DataflowProcessRunRequest{
				AnalysisID: o.analysisID,
				NodeID:     o.nodeID,
				Inputs:     inputs,
				Reason:     "input-ready",
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *InputOperator) pickInputValueLocked(key string, queue []any) (any, bool) {
	if len(queue) == 0 {
		return nil, false
	}
	// Nextflow-like value semantics: singleton value input in a multi-input join
	// is reusable across multiple tuples emitted by other inputs.
	if len(o.requiredKeys) > 1 && len(queue) == 1 && o.receivedCnt[key] == 1 {
		return queue[0], false
	}
	return queue[0], true
}

func (o *InputOperator) isInputKeyClosed(inputKey string) bool {
	if o.BaseOperator == nil {
		return false
	}
	o.BaseOperator.mu.Lock()
	defer o.BaseOperator.mu.Unlock()

	found := false
	for channelID, key := range o.inputByChID {
		if key != inputKey {
			continue
		}
		found = true
		if !o.BaseOperator.inputClosed[channelID] {
			return false
		}
	}
	return found
}

func (o *InputOperator) ready() bool {
	if len(o.requiredKeys) == 0 {
		return false
	}
	for _, key := range o.requiredKeys {
		if len(o.buffers[key]) == 0 {
			return false
		}
	}
	return true
}
