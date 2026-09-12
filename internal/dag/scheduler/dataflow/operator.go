package dataflow

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// DataflowSignal is the message exchanged through channels/operators.
type DataflowSignal struct {
	ChannelID string
	Value     any
	Closed    bool
}

// DataflowOperator receives channel signals and decides whether a process instance
// should be materialized.
type DataflowOperator interface {
	Notify(ctx context.Context, signal DataflowSignal) error
}

type baseOperatorConfig struct {
	inputByChID     map[string]string
	outputChannels  map[string]*DataflowChannel
	onData          func(ctx context.Context, signal DataflowSignal) error
	canFinish       func() bool
	beforeFinish    func(ctx context.Context) error
	ignoreUnknownCh bool
}

// BaseOperator defines a template Notify flow:
// 1) update close-state on closed signal
// 2) delegate data handling to onData hook
// 3) attempt finish sequence if all inputs are closed
type BaseOperator struct {
	mu sync.Mutex

	inputByChID    map[string]string
	inputClosed    map[string]bool
	outputChannels map[string]*DataflowChannel

	finished bool

	onData       func(ctx context.Context, signal DataflowSignal) error
	canFinish    func() bool
	beforeFinish func(ctx context.Context) error

	ignoreUnknownCh bool
}

func newBaseOperator(cfg baseOperatorConfig) *BaseOperator {
	normalizedInputByChID := make(map[string]string, len(cfg.inputByChID))
	inputClosed := make(map[string]bool, len(cfg.inputByChID))
	for channelID, inputKey := range cfg.inputByChID {
		normalizedChannelID := strings.TrimSpace(channelID)
		if normalizedChannelID == "" {
			continue
		}
		normalizedInputByChID[normalizedChannelID] = strings.TrimSpace(inputKey)
		inputClosed[normalizedChannelID] = false
	}

	outputs := make(map[string]*DataflowChannel, len(cfg.outputChannels))
	for channelID, ch := range cfg.outputChannels {
		normalizedChannelID := strings.TrimSpace(channelID)
		if normalizedChannelID == "" || ch == nil {
			continue
		}
		outputs[normalizedChannelID] = ch
	}

	return &BaseOperator{
		inputByChID:     normalizedInputByChID,
		inputClosed:     inputClosed,
		outputChannels:  outputs,
		onData:          cfg.onData,
		canFinish:       cfg.canFinish,
		beforeFinish:    cfg.beforeFinish,
		ignoreUnknownCh: cfg.ignoreUnknownCh,
	}
}

func (o *BaseOperator) Notify(ctx context.Context, signal DataflowSignal) error {
	if o.isFinished() {
		return nil
	}

	channelID := strings.TrimSpace(signal.ChannelID)
	if channelID == "" {
		return nil
	}

	if o.ignoreUnknownCh && !o.hasInput(channelID) {
		return nil
	}

	if signal.Closed {
		o.markInputClosed(channelID)
	} else if o.onData != nil {
		signal.ChannelID = channelID
		if err := o.onData(ctx, signal); err != nil {
			return err
		}
	}

	return o.tryFinish(ctx)
}

func (o *BaseOperator) isFinished() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.finished
}

func (o *BaseOperator) IsFinished() bool {
	if o == nil {
		return false
	}
	return o.isFinished()
}

func (o *BaseOperator) hasInput(channelID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	_, ok := o.inputByChID[channelID]
	return ok
}

func (o *BaseOperator) markInputClosed(channelID string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if _, ok := o.inputClosed[channelID]; ok {
		o.inputClosed[channelID] = true
	}
}

func (o *BaseOperator) allInputClosed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	if len(o.inputClosed) == 0 {
		return false
	}
	for _, closed := range o.inputClosed {
		if !closed {
			return false
		}
	}
	return true
}

func (o *BaseOperator) tryFinish(ctx context.Context) error {
	if o.isFinished() {
		return nil
	}

	if !o.allInputClosed() {
		return nil
	}
	if o.canFinish != nil && !o.canFinish() {
		return nil
	}
	if o.beforeFinish != nil {
		if err := o.beforeFinish(ctx); err != nil {
			return err
		}
	}
	o.markFinished()
	return nil
}

func (o *BaseOperator) markFinished() {
	o.mu.Lock()
	o.finished = true
	o.mu.Unlock()
}

type operatorCommonConfig struct {
	analysisID   int64
	nodeID       string
	inputByChID  map[string]string
	requiredKeys []string
	runtime      DataflowProcessRuntime
}

func newOperatorCommonConfig(
	analysisID int64,
	nodeID string,
	inputByChannelID map[string]string,
	runtime DataflowProcessRuntime,
) operatorCommonConfig {
	normalizedInputByChID := make(map[string]string, len(inputByChannelID))
	required := make([]string, 0, len(inputByChannelID))
	for channelID, inputKey := range inputByChannelID {
		normalizedChannelID := strings.TrimSpace(channelID)
		normalizedInputKey := strings.TrimSpace(inputKey)
		normalizedInputByChID[normalizedChannelID] = normalizedInputKey
		required = appendUniqueString(required, normalizedInputKey)
	}
	sort.Strings(required)

	return operatorCommonConfig{
		analysisID:   analysisID,
		nodeID:       strings.TrimSpace(nodeID),
		inputByChID:  normalizedInputByChID,
		requiredKeys: required,
		runtime:      runtime,
	}
}

func newDataflowOperator(
	analysisID int64,
	proc DataflowProcessSpec,
	inputByChannelID map[string]string,
	outputChannels map[string]*DataflowChannel,
	runtime DataflowProcessRuntime,
) DataflowOperator {
	if inputByChannelID == nil {
		inputByChannelID = map[string]string{}
	}
	if outputChannels == nil {
		outputChannels = map[string]*DataflowChannel{}
	}

	type operatorBuilder func(
		analysisID int64,
		nodeID string,
		proc DataflowProcessSpec,
		inputByChannelID map[string]string,
		outputChannels map[string]*DataflowChannel,
		runtime DataflowProcessRuntime,
	) DataflowOperator

	builders := map[string]operatorBuilder{
		string(dataflowOperatorTypeGather): func(
			analysisID int64,
			nodeID string,
			proc DataflowProcessSpec,
			inputByChannelID map[string]string,
			outputChannels map[string]*DataflowChannel,
			runtime DataflowProcessRuntime,
		) DataflowOperator {
			return newGatherOperator(
				analysisID,
				nodeID,
				inputByChannelID,
				outputChannels,
				proc.GatherField,
				proc.GatherMode,
				runtime,
			)
		},
		string(dataflowOperatorTypeScatter): func(
			analysisID int64,
			nodeID string,
			proc DataflowProcessSpec,
			inputByChannelID map[string]string,
			outputChannels map[string]*DataflowChannel,
			runtime DataflowProcessRuntime,
		) DataflowOperator {
			return newScatterOperator(
				analysisID,
				nodeID,
				inputByChannelID,
				outputChannels,
				proc.ScatterField,
				proc.ScatterMode,
				runtime,
			)
		},
	}

	nodeID := strings.TrimSpace(proc.NodeID)
	operatorType := strings.ToLower(strings.TrimSpace(proc.OperatorType))
	if builder, ok := builders[operatorType]; ok {
		return builder(analysisID, nodeID, proc, inputByChannelID, outputChannels, runtime)
	}
	return newInputOperator(analysisID, nodeID, inputByChannelID, outputChannels, runtime)
}
