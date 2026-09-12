package dataflow

import (
	"context"
	"sort"
	"strings"
)

func (k *dataflowKernel) bootstrapSourceProcesses(ctx context.Context) error {
	for _, proc := range k.sourceNodes {
		if k.runtime == nil {
			continue
		}
		if err := k.submitSourceProcess(ctx, proc); err != nil {
			return err
		}
	}

	k.stateMu.Lock()
	k.sourcesBootstrapped = true
	k.stateMu.Unlock()

	for _, proc := range k.sourceNodes {
		if err := k.tryCloseOutputChannelsForNode(ctx, proc.NodeID); err != nil {
			return err
		}
	}
	if err := k.reconcileOutputChannelClosures(ctx); err != nil {
		return err
	}
	return nil
}

func (k *dataflowKernel) submitSourceProcess(ctx context.Context, proc DataflowProcessSpec) error {
	if k.runtime == nil {
		return nil
	}

	operatorType := strings.ToLower(strings.TrimSpace(proc.OperatorType))
	scatterField := strings.TrimSpace(proc.ScatterField)
	scatterMode := strings.ToLower(strings.TrimSpace(proc.ScatterMode))

	// Scatter source values are always driven by parse_analysis_result[scatter.field]
	// rather than proc.InputKeys.
	if operatorType == string(dataflowOperatorTypeScatter) && scatterField != "" {
		raw, ok := k.params[scatterField]
		if !ok {
			return k.submitSourceProcessFallback(ctx, proc)
		}
		values, isSlice := toAnySlice(raw)
		if !isSlice {
			values = []any{raw}
		}

		sourceValuesByInputKey := map[string][]any{}
		switch scatterMode {
		case "list":
			sourceValuesByInputKey[scatterField] = []any{values}
		default:
			sourceValuesByInputKey[scatterField] = []any{values}
		}
		return k.emitSourceValuesToOperator(ctx, proc, sourceValuesByInputKey)
	}

	sourceValuesByInputKey := make(map[string][]any, len(proc.InputKeys))
	for _, key := range proc.InputKeys {
		inputKey := strings.TrimSpace(key)
		if inputKey == "" {
			continue
		}
		raw, ok := k.params[inputKey]
		if !ok {
			continue
		}
		sourceValuesByInputKey[inputKey] = []any{raw}
	}

	if len(sourceValuesByInputKey) == 0 {
		return k.submitSourceProcessFallback(ctx, proc)
	}
	return k.emitSourceValuesToOperator(ctx, proc, sourceValuesByInputKey)
}

func (k *dataflowKernel) emitSourceValuesToOperator(
	ctx context.Context,
	proc DataflowProcessSpec,
	sourceValuesByInputKey map[string][]any,
) error {
	nodeID := strings.TrimSpace(proc.NodeID)
	inputByChannelID := make(map[string]string, len(sourceValuesByInputKey))
	channelIDs := make([]string, 0, len(sourceValuesByInputKey))
	for inputKey := range sourceValuesByInputKey {
		channelID := dataflowSourceChannelID(nodeID, inputKey)
		inputByChannelID[channelID] = inputKey
		channelIDs = append(channelIDs, channelID)
	}
	sort.Strings(channelIDs)

	outputChannels := k.outputChannelsForNode(nodeID)
	op := newDataflowOperator(k.analysisID, proc, inputByChannelID, outputChannels, k.runtime)
	if op == nil {
		return nil
	}

	channels := make([]*DataflowChannel, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		ch := newDataflowChannel(channelID)
		ch.Subscribe(op)
		channels = append(channels, ch)
	}

	for _, ch := range channels {
		channelID := ch.ID()
		inputKey := inputByChannelID[channelID]
		for _, value := range sourceValuesByInputKey[inputKey] {
			if err := ch.Emit(ctx, value); err != nil {
				return err
			}
		}
	}

	for _, ch := range channels {
		if err := ch.Close(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (k *dataflowKernel) submitSourceProcessFallback(ctx context.Context, proc DataflowProcessSpec) error {
	inputs := make(map[string]any, len(proc.InputKeys))
	for _, key := range proc.InputKeys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		value, ok := k.params[trimmed]
		if !ok {
			continue
		}
		inputs[trimmed] = value
	}

	return k.runtime.SubmitProcessInstance(ctx, DataflowProcessRunRequest{
		AnalysisID: k.analysisID,
		NodeID:     strings.TrimSpace(proc.NodeID),
		Inputs:     inputs,
		Reason:     "source-bootstrap",
	})
}
