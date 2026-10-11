package dataflow

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/compiler"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/nodebuild"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

type dataflowKernel struct {
	analysisID          int64
	repo                interfaces.AnalysisRepository
	channels            map[string]*DataflowChannel
	channelSpecByID     map[string]DataflowChannelSpec
	outgoingByNode      map[string][]string
	processByNode       map[string]DataflowProcessSpec
	operators           map[string]DataflowOperator
	sourceNodes         []DataflowProcessSpec
	sourceNodeSet       map[string]struct{}
	params              map[string]any
	runtime             DataflowProcessRuntime
	stateMu             sync.Mutex
	submittedByNode     map[string]int
	completedByNode     map[string]int
	outputsClosed       map[string]bool
	emittingByNode      map[string]int
	sourcesBootstrapped bool
	// analysisRequestParam is the analysis's raw request_param (parsed). Each node's
	// request_param is derived from it by keeping only that node's input handles, so
	// V3 matches the compiler / V2 口径.
	analysisRequestParam map[string]any
	// parser turns a select-assay input's original form into the structured
	// resolved_inputs recipe. nil skips parsing (request_param is still written).
	parser dagruntime.NodeRequestParser
}

type finishAwareDataflowOperator interface {
	IsFinished() bool
}

func (k *dataflowKernel) checkFinished(runtimeTasks int) bool {
	if runtimeTasks > 0 {
		return false
	}
	if !k.allChannelsClosed() {
		return false
	}
	if !k.allOperatorsFinished() {
		return false
	}
	return true
}

func (k *dataflowKernel) allChannelsClosed() bool {
	for _, ch := range k.channels {
		if ch == nil {
			continue
		}
		if !ch.IsClosed() {
			return false
		}
	}
	return true
}

func (k *dataflowKernel) allOperatorsFinished() bool {
	for _, op := range k.operators {
		if !isDataflowOperatorFinished(op) {
			return false
		}
	}
	return true
}

func isDataflowOperatorFinished(op DataflowOperator) bool {
	aware, ok := op.(finishAwareDataflowOperator)
	if !ok || aware == nil {
		return false
	}
	return aware.IsFinished()
}

func (k *dataflowKernel) outputChannelsForNode(nodeID string) map[string]*DataflowChannel {
	result := map[string]*DataflowChannel{}
	normalizedNodeID := strings.TrimSpace(nodeID)
	if normalizedNodeID == "" {
		return result
	}

	channelIDs := k.outgoingByNode[normalizedNodeID]
	for _, channelID := range channelIDs {
		normalizedChannelID := strings.TrimSpace(channelID)
		if normalizedChannelID == "" {
			continue
		}
		if ch, exists := k.channels[normalizedChannelID]; exists && ch != nil {
			result[normalizedChannelID] = ch
		}
	}
	return result
}

func newDataflowKernel(spec DataflowGraphSpec, runtime DataflowProcessRuntime, params map[string]any) *dataflowKernel {
	channels := make(map[string]*DataflowChannel, len(spec.Channels))
	channelSpecByID := make(map[string]DataflowChannelSpec, len(spec.Channels))
	outgoingByNode := map[string][]string{}
	for _, ch := range spec.Channels {
		id := strings.TrimSpace(ch.ChannelID)
		if id == "" {
			continue
		}
		channelSpecByID[id] = ch
		if _, exists := channels[id]; !exists {
			channels[id] = newDataflowChannel(id)
		}
		fromNodeID := strings.TrimSpace(ch.FromNodeID)
		if fromNodeID != "" {
			outgoingByNode[fromNodeID] = appendUniqueString(outgoingByNode[fromNodeID], id)
		}
	}

	upstreamByNode := map[string]map[string]DataflowChannelSpec{}
	for _, ch := range spec.Channels {
		nodeID := strings.TrimSpace(ch.ToNodeID)
		if nodeID == "" || strings.TrimSpace(ch.ChannelID) == "" {
			continue
		}
		if _, exists := upstreamByNode[nodeID]; !exists {
			upstreamByNode[nodeID] = map[string]DataflowChannelSpec{}
		}
		upstreamByNode[nodeID][ch.ChannelID] = ch
	}

	k := &dataflowKernel{
		analysisID:      spec.AnalysisID,
		channels:        channels,
		channelSpecByID: channelSpecByID,
		outgoingByNode:  outgoingByNode,
		processByNode:   make(map[string]DataflowProcessSpec, len(spec.Processes)),
		operators:       make(map[string]DataflowOperator, len(spec.Processes)),
		sourceNodeSet:   make(map[string]struct{}),
		params:          params,
		runtime:         runtime,
		submittedByNode: make(map[string]int, len(spec.Processes)),
		completedByNode: make(map[string]int, len(spec.Processes)),
		outputsClosed:   make(map[string]bool, len(spec.Processes)),
		emittingByNode:  make(map[string]int, len(spec.Processes)),
	}

	for _, proc := range spec.Processes {
		nodeID := strings.TrimSpace(proc.NodeID)
		if nodeID == "" {
			continue
		}
		k.processByNode[nodeID] = proc
		upstream := upstreamByNode[nodeID]
		if len(upstream) == 0 {
			k.sourceNodes = append(k.sourceNodes, proc)
			continue
		}

		inputByChannelID := map[string]string{}
		for channelID, channelSpec := range upstream {
			inputKey := strings.TrimSpace(channelSpec.ToPort)
			if inputKey == "" {
				inputKey = strings.TrimSpace(channelSpec.FromNodeID)
			}
			inputByChannelID[channelID] = inputKey
		}

		outputChannels := k.outputChannelsForNode(nodeID)
		op := newDataflowOperator(spec.AnalysisID, proc, inputByChannelID, outputChannels, runtime)
		if op == nil {
			continue
		}
		for channelID := range inputByChannelID {
			if ch, exists := k.channels[channelID]; exists {
				ch.Subscribe(op)
			}
		}
		k.operators[nodeID] = op
	}

	sort.Slice(k.sourceNodes, func(i, j int) bool {
		return strings.TrimSpace(k.sourceNodes[i].NodeID) < strings.TrimSpace(k.sourceNodes[j].NodeID)
	})
	for _, proc := range k.sourceNodes {
		nodeID := strings.TrimSpace(proc.NodeID)
		if nodeID == "" {
			continue
		}
		k.sourceNodeSet[nodeID] = struct{}{}
	}
	for nodeID := range k.outgoingByNode {
		sort.Strings(k.outgoingByNode[nodeID])
	}
	return k
}

func (k *dataflowKernel) adjustSubmittedCount(nodeID string, delta int) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" || delta == 0 {
		return
	}
	k.stateMu.Lock()
	defer k.stateMu.Unlock()
	next := k.submittedByNode[nodeID] + delta
	if next < 0 {
		next = 0
	}
	k.submittedByNode[nodeID] = next
}

func (k *dataflowKernel) markNodeCompleted(nodeID string) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return
	}
	k.stateMu.Lock()
	k.completedByNode[nodeID]++
	k.stateMu.Unlock()
}

func (k *dataflowKernel) tryCloseOutputChannelsForNode(ctx context.Context, nodeID string) error {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil
	}

	k.stateMu.Lock()
	if k.outputsClosed[nodeID] {
		k.stateMu.Unlock()
		return nil
	}
	submitted := k.submittedByNode[nodeID]
	completed := k.completedByNode[nodeID]
	emitting := k.emittingByNode[nodeID]
	k.stateMu.Unlock()

	if emitting > 0 {
		return nil
	}
	if submitted == 0 || completed < submitted {
		return nil
	}
	if !k.noMoreSubmissionsExpected(nodeID) {
		return nil
	}

	for _, ch := range k.outputChannelsForNode(nodeID) {
		if ch == nil {
			continue
		}
		if err := ch.Close(ctx); err != nil {
			return err
		}
	}

	k.stateMu.Lock()
	k.outputsClosed[nodeID] = true
	k.stateMu.Unlock()
	return nil
}

func (k *dataflowKernel) beginNodeEmit(nodeID string) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return
	}
	k.stateMu.Lock()
	k.emittingByNode[nodeID]++
	k.stateMu.Unlock()
}

func (k *dataflowKernel) endNodeEmit(nodeID string) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return
	}
	k.stateMu.Lock()
	if k.emittingByNode[nodeID] > 0 {
		k.emittingByNode[nodeID]--
	}
	k.stateMu.Unlock()
}

func (k *dataflowKernel) reconcileOutputChannelClosures(ctx context.Context) error {
	for nodeID := range k.processByNode {
		if err := k.tryCloseOutputChannelsForNode(ctx, nodeID); err != nil {
			return err
		}
	}
	return nil
}

func (k *dataflowKernel) noMoreSubmissionsExpected(nodeID string) bool {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return false
	}
	if _, ok := k.sourceNodeSet[nodeID]; ok {
		k.stateMu.Lock()
		bootstrapped := k.sourcesBootstrapped
		k.stateMu.Unlock()
		return bootstrapped
	}
	op, ok := k.operators[nodeID]
	if !ok || op == nil {
		return false
	}
	return isDataflowOperatorFinished(op)
}

func (k *dataflowKernel) buildAnalysisNodePersistParams(ctx context.Context, req DataflowProcessRunRequest) (*DataflowAnalysisNodePersistParams, bool) {
	nodeID := strings.TrimSpace(req.NodeID)
	if nodeID == "" {
		return nil, false
	}
	proc, exists := k.processByNode[nodeID]
	if !exists {
		return nil, false
	}

	analysisID := req.AnalysisID
	if analysisID <= 0 {
		analysisID = k.analysisID
	}

	resolvedInputs := cloneInputs(proc.ResolvedIn)
	for key, value := range req.Inputs {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		resolvedInputs[trimmed] = value
	}

	// request_param 与 V2/compiler 同口径：analysis.request_param 按本节点输入句柄过滤。
	requestParam := compiler.FilterRequestParamByHandles(k.analysisRequestParam, proc.Inputs)
	// 再把 select-assay 输入重写成原始形态 {"files": "<id>"} 并用解析结果覆盖
	// resolved_inputs（V3 的 req.Inputs 已经带了上游运行期原始值，所以这里只做 select-assay
	// 重写，不重复合并上游产物）。
	resolvedInputs, requestParam = k.bindSelectAssayInputs(ctx, analysisID, nodeID, proc, resolvedInputs, requestParam)

	return &DataflowAnalysisNodePersistParams{
		AnalysisID:      analysisID,
		NodeID:          nodeID,
		InputHash:       nodebuild.InstanceInputHash(nodeID, types.JSONMap(resolvedInputs)),
		NodeName:        strings.TrimSpace(proc.NodeName),
		SampleID:        strings.TrimSpace(proc.SampleID),
		ScriptID:        strings.TrimSpace(proc.ScriptID),
		InputsPatterns:  cloneInputs(proc.Inputs),
		OutputPatterns:  cloneInputs(proc.Outputs),
		ResolvedInputs:  resolvedInputs,
		RequestParam:    requestParam,
		ResolvedOutputs: cloneInputs(proc.ResolvedOut),
		UpstreamIDs:     append([]string(nil), proc.UpstreamIDs...),
		DownstreamIDs:   append([]string(nil), proc.Downstream...),
		Executor:        strings.TrimSpace(proc.Executor),
		Retry:           proc.Retry,
		MaxRetry:        proc.MaxRetry,
		RerunReason:     strings.TrimSpace(proc.RerunReason),
		Status:          "ready",
		SubmitReason:    strings.TrimSpace(req.Reason),
	}, true
}

// bindSelectAssayInputs rewrites this node's select-assay inputs from the original
// input form into {"files": "<go_file id>"} and replaces the matching resolved_inputs
// with the structured recipe the parser builds.
//
// V3 does not persist analysis_edges, so the incoming edges are synthesized from the
// in-memory channel specs; the upstream node rows (which carry output_file_ids) are
// read from the repository. This is the V3-native equivalent of V2's
// materialization-time binding.
func (k *dataflowKernel) bindSelectAssayInputs(
	ctx context.Context,
	analysisID int64,
	nodeID string,
	proc DataflowProcessSpec,
	resolvedInputs map[string]any,
	requestParam map[string]any,
) (map[string]any, map[string]any) {
	if k == nil || k.repo == nil || len(k.channelSpecByID) == 0 {
		return resolvedInputs, requestParam
	}

	incoming := make([]*types.AnalysisEdge, 0)
	upstream := map[string]*types.AnalysisNode{}
	for _, ch := range k.channelSpecByID {
		if strings.TrimSpace(ch.ToNodeID) != nodeID {
			continue
		}
		sourceNodeID := strings.TrimSpace(ch.FromNodeID)
		if sourceNodeID == "" {
			continue
		}
		incoming = append(incoming, &types.AnalysisEdge{
			AnalysisID:   analysisID,
			SourceNode:   sourceNodeID,
			TargetNode:   strings.TrimSpace(ch.ToNodeID),
			SourceHandle: strings.TrimSpace(ch.FromPort),
			TargetHandle: strings.TrimSpace(ch.ToPort),
		})
		if _, ok := upstream[sourceNodeID]; ok {
			continue
		}
		source, err := k.repo.GetAnalysisNodeByNodeID(ctx, analysisID, sourceNodeID)
		if err != nil || source == nil {
			continue
		}
		upstream[sourceNodeID] = source
	}
	if len(incoming) == 0 {
		return resolvedInputs, requestParam
	}

	target := &types.AnalysisNode{
		AnalysisID:     analysisID,
		NodeID:         nodeID,
		ScriptID:       parseScriptID(proc.ScriptID),
		InputsPatterns: types.JSONMap(proc.Inputs),
		ResolvedInputs: types.JSONMap(resolvedInputs),
		RequestParam:   encodeRequestParamMap(requestParam),
	}
	bindings := dagruntime.BuildUpstreamInputBindings(incoming, upstream)
	if !dagruntime.ApplySelectAssayBindings(ctx, k.parser, target, bindings) {
		return resolvedInputs, requestParam
	}
	return map[string]any(target.ResolvedInputs), decodeRequestParamMap(target.RequestParam)
}

// parseScriptID parses the process spec's string script id into the numeric primary
// key the parser needs. Invalid / empty values yield 0.
func parseScriptID(raw string) int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// encodeRequestParamMap renders a request_param map into the JSON string stored on the
// node. An empty map yields "" so a caller that never set it leaves the column alone.
func encodeRequestParamMap(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	buf, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(buf)
}

// decodeRequestParamMap parses a node request_param string back into a map.
func decodeRequestParamMap(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func (k *dataflowKernel) emitToDownstream(ctx context.Context, fromNodeID string, value any) error {
	channelIDs := k.outgoingByNode[strings.TrimSpace(fromNodeID)]
	if len(channelIDs) == 0 {
		return nil
	}
	for _, channelID := range channelIDs {
		ch, exists := k.channels[channelID]
		if !exists || ch == nil {
			continue
		}
		emitValue := value
		if outputs, ok := value.(map[string]any); ok {
			channelSpec, hasSpec := k.channelSpecByID[channelID]
			fromPort := ""
			if hasSpec {
				fromPort = strings.TrimSpace(channelSpec.FromPort)
			}
			if fromPort != "" {
				v, exists := outputs[fromPort]
				if !exists {
					continue
				}
				emitValue = v
			}
		}
		if err := ch.Emit(ctx, emitValue); err != nil {
			return err
		}
	}
	return nil
}

func (k *dataflowKernel) onNodeCompleted(ctx context.Context, analysisNodeID int64) error {
	if k.repo == nil {
		return nil
	}
	if analysisNodeID <= 0 {
		return nil
	}
	node, err := k.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
	if err != nil {
		return err
	}
	if node == nil || node.AnalysisID != k.analysisID {
		return nil
	}
	nodeID := strings.TrimSpace(node.NodeID)
	if nodeID == "" {
		return nil
	}
	return k.completeNode(ctx, nodeID, map[string]any(node.ResolvedOutputs))
}

// onInstanceReused advances the kernel when a submit resolved to an already
// persisted successful node (cache reuse) instead of dispatching a new instance.
// It mirrors the completion path using the persisted outputs so the instance is
// counted as completed and its outputs still reach downstream operators.
func (k *dataflowKernel) onInstanceReused(ctx context.Context, node *types.AnalysisNode) error {
	if k == nil || node == nil {
		return nil
	}
	if node.AnalysisID != k.analysisID {
		return nil
	}
	nodeID := strings.TrimSpace(node.NodeID)
	if nodeID == "" {
		return nil
	}
	return k.completeNode(ctx, nodeID, map[string]any(node.ResolvedOutputs))
}

// completeNode marks one instance of nodeID as completed, propagates its outputs
// downstream and then advances channel closure. It is shared by the real
// completion-event path and the cache-reuse path so both converge identically.
func (k *dataflowKernel) completeNode(ctx context.Context, nodeID string, outputs map[string]any) error {
	k.markNodeCompleted(nodeID)
	k.beginNodeEmit(nodeID)
	if err := k.emitToDownstream(ctx, nodeID, outputs); err != nil {
		k.endNodeEmit(nodeID)
		return err
	}
	k.endNodeEmit(nodeID)
	if err := k.tryCloseOutputChannelsForNode(ctx, nodeID); err != nil {
		return err
	}
	return k.reconcileOutputChannelClosures(ctx)
}

func (k *dataflowKernel) onNodeFailed(ctx context.Context, analysisNodeID int64) error {
	if k.repo == nil {
		return nil
	}
	if analysisNodeID <= 0 {
		return nil
	}
	node, err := k.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
	if err != nil {
		return err
	}
	if node == nil || node.AnalysisID != k.analysisID {
		return nil
	}
	nodeID := strings.TrimSpace(node.NodeID)
	if nodeID == "" {
		return nil
	}
	k.markNodeCompleted(nodeID)
	if err := k.tryCloseOutputChannelsForNode(ctx, nodeID); err != nil {
		return err
	}
	return k.reconcileOutputChannelClosures(ctx)
}

func (k *dataflowKernel) closeAll(ctx context.Context) error {
	for _, ch := range k.channels {
		if err := ch.Close(ctx); err != nil {
			return err
		}
	}
	return nil
}
