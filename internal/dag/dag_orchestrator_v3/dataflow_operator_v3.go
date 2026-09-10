package orchestratorv3

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
