package dataflow

import (
	"context"
	"strings"
	"sync"
)

// ScatterOperator expands one input field according to scatter mode.
// Supported modes: each, list.
type ScatterOperator struct {
	*BaseOperator
	analysisID   int64
	nodeID       string
	inputByChID  map[string]string
	requiredKeys []string
	buffers      map[string][]any
	scatterField string
	scatterMode  string
	runtime      DataflowProcessRuntime
	mu           sync.Mutex
}

func newScatterOperator(
	analysisID int64,
	nodeID string,
	inputByChannelID map[string]string,
	outputChannels map[string]*DataflowChannel,
	scatterField string,
	scatterMode string,
	runtime DataflowProcessRuntime,
) *ScatterOperator {
	common := newOperatorCommonConfig(analysisID, nodeID, inputByChannelID, runtime)
	o := &ScatterOperator{
		analysisID:   common.analysisID,
		nodeID:       common.nodeID,
		inputByChID:  common.inputByChID,
		requiredKeys: common.requiredKeys,
		buffers:      make(map[string][]any, len(common.requiredKeys)),
		scatterField: strings.TrimSpace(scatterField),
		scatterMode:  strings.ToLower(strings.TrimSpace(scatterMode)),
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

func (o *ScatterOperator) handleData(ctx context.Context, signal DataflowSignal) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	inputKey := o.inputByChID[signal.ChannelID]

	o.buffers[inputKey] = append(o.buffers[inputKey], signal.Value)
	for o.ready() {
		inputs := make(map[string]any, len(o.requiredKeys))
		for _, key := range o.requiredKeys {
			queue := o.buffers[key]
			inputs[key] = queue[0]
			o.buffers[key] = queue[1:]
		}
		if err := o.submitScatter(ctx, inputs); err != nil {
			return err
		}
	}
	return nil
}

func (o *ScatterOperator) ready() bool {
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

func (o *ScatterOperator) submitScatter(ctx context.Context, inputs map[string]any) error {
	if o.runtime == nil {
		return nil
	}
	raw, exists := inputs[o.scatterField]
	if !exists || o.scatterField == "" {
		return o.runtime.SubmitProcessInstance(ctx, DataflowProcessRunRequest{
			AnalysisID: o.analysisID,
			NodeID:     o.nodeID,
			Inputs:     inputs,
			Reason:     "scatter-ready",
		})
	}

	values, isSlice := toAnySlice(raw)
	switch o.scatterMode {
	case "each":
		if !isSlice {
			return o.runtime.SubmitProcessInstance(ctx, DataflowProcessRunRequest{
				AnalysisID: o.analysisID,
				NodeID:     o.nodeID,
				Inputs:     inputs,
				Reason:     "scatter-each-ready",
			})
		}
		for _, item := range values {
			expanded := cloneInputs(inputs)
			expanded[o.scatterField] = item
			if err := o.runtime.SubmitProcessInstance(ctx, DataflowProcessRunRequest{
				AnalysisID: o.analysisID,
				NodeID:     o.nodeID,
				Inputs:     expanded,
				Reason:     "scatter-each-ready",
			}); err != nil {
				return err
			}
		}
		return nil
	case "list":
		expanded := cloneInputs(inputs)
		if !isSlice {
			expanded[o.scatterField] = []any{raw}
		} else {
			expanded[o.scatterField] = values
		}
		return o.runtime.SubmitProcessInstance(ctx, DataflowProcessRunRequest{
			AnalysisID: o.analysisID,
			NodeID:     o.nodeID,
			Inputs:     expanded,
			Reason:     "scatter-list-ready",
		})
	default:
		return o.runtime.SubmitProcessInstance(ctx, DataflowProcessRunRequest{
			AnalysisID: o.analysisID,
			NodeID:     o.nodeID,
			Inputs:     inputs,
			Reason:     "scatter-ready",
		})
	}
}
