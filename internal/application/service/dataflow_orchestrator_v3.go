package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gobravedev/gobrave/internal/config"
	dagruntime "github.com/gobravedev/gobrave/internal/dag"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/manager"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
	"github.com/google/uuid"
)

// dataflowDagOrchestratorV3 is the framework entry for a Nextflow-like
// data-driven scheduler.
//
// Current stage:
// - Build V3 dataflow planning skeleton.
// - Keep existing frontend payload, executor path, and analysis persistence untouched.
// - Delegate runtime execution to V2 for safe rollout.
type dataflowDagOrchestratorV3 struct {
	bus          event.Bus
	repo         interfaces.AnalysisRepository
	workflowRepo interfaces.WorkflowRepository
	projectRepo  interfaces.ProjectRepository
	analysisRepo interfaces.AnalysisRepository
	containerMgr *manager.ContainerManager
	dispatcher   *dagruntime.NodeDispatcher
	// runScriptBuilders map[string]prepare.RunScriptBuilder
	cfg *config.Config
}

func NewDataflowDagOrchestratorV3(
	repo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	analysisRepo interfaces.AnalysisRepository,
	containerMgr *manager.ContainerManager,
	projectRepo interfaces.ProjectRepository,
	dispatcher *dagruntime.NodeDispatcher,
	// runScriptBuilders map[string]prepare.RunScriptBuilder,
	cfg *config.Config,
	bus event.Bus,
) interfaces.DataflowDagOrchestrator {
	return &dataflowDagOrchestratorV3{
		bus:          bus,
		repo:         repo,
		workflowRepo: workflowRepo,
		dispatcher:   dispatcher,
		containerMgr: containerMgr,
		projectRepo:  projectRepo,
		// runScriptBuilders: runScriptBuilders,
		cfg: cfg,
	}
}

type dataflowRuntimeEventHandler struct {
	analysisID int64
	events     chan<- dagruntime.RuntimeEvent
}

func (h *dataflowRuntimeEventHandler) Handle(evt event.Event) {
	runtimeEvt, ok := evt.(dagruntime.RuntimeEvent)
	if !ok || runtimeEvt.AnalysisID != h.analysisID {
		return
	}
	select {
	case h.events <- runtimeEvt:
	default:
	}
}

// DataflowGraphSpec is a V3 planning view converted from dag_definition.
// It is intentionally lightweight for the first framework milestone.
type DataflowGraphSpec struct {
	AnalysisID int64
	Processes  []DataflowProcessSpec
	Channels   []DataflowChannelSpec
}

// DataflowProcessSpec represents a process template (not a persisted node instance).
type DataflowProcessSpec struct {
	NodeID       string
	NodeName     string
	SampleID     string
	ScriptID     string
	InputKeys    []string
	UpstreamIDs  []string
	Downstream   []string
	Inputs       map[string]any
	Outputs      map[string]any
	Params       map[string]any
	ResolvedIn   map[string]any
	ResolvedOut  map[string]any
	Executor     string
	Retry        int
	MaxRetry     int
	RerunReason  string
	OperatorType string
	ScatterField string
	ScatterMode  string
	GatherField  string
	GatherMode   string
}

// DataflowAnalysisNodePersistParams is a normalized payload that contains
// the fields needed to create an analysis_node record.
//
// It is assembled at the runtime submit boundary and will be consumed by
// a persistent runtime implementation in the next step.
type DataflowAnalysisNodePersistParams struct {
	AnalysisID      int64
	NodeID          string
	InputHash       string
	NodeName        string
	SampleID        string
	ScriptID        string
	InputsPatterns  map[string]any
	OutputPatterns  map[string]any
	Params          map[string]any
	ResolvedInputs  map[string]any
	ResolvedOutputs map[string]any
	UpstreamIDs     []string
	DownstreamIDs   []string
	Executor        string
	Retry           int
	MaxRetry        int
	RerunReason     string
	Status          string
	SubmitReason    string
	WorkspaceDir    string
	OutputDir       string
	CommandPath     string
	ParamsPath      string
	LogPath         string
}

// DataflowChannelSpec represents a logical edge/channel in the dataflow graph.
type DataflowChannelSpec struct {
	ChannelID  string
	FromNodeID string
	ToNodeID   string
	FromPort   string
	ToPort     string
}

type dataflowChannelState string

type dataflowOperatorType string

const (
	dataflowChannelStateOpen   dataflowChannelState = "open"
	dataflowChannelStateClosed dataflowChannelState = "closed"

	dataflowOperatorTypeInput   dataflowOperatorType = "input"
	dataflowOperatorTypeGather  dataflowOperatorType = "gather"
	dataflowOperatorTypeScatter dataflowOperatorType = "scatter"
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

// DataflowProcessRuntime is the bridge from operator decisions to concrete process launch.
type DataflowProcessRuntime interface {
	SubmitProcessInstance(ctx context.Context, req DataflowProcessRunRequest) error
}

type DataflowProcessRunRequest struct {
	AnalysisID int64
	NodeID     string
	Inputs     map[string]any
	Reason     string
}

type loggingDataflowRuntime struct {
	onSubmitted func(ctx context.Context, req DataflowProcessRunRequest) error
}

func (r *loggingDataflowRuntime) SubmitProcessInstance(ctx context.Context, req DataflowProcessRunRequest) error {
	logger.Infof(ctx,
		"[DataflowDagOrchestratorV3] submit process instance, analysis_id=%s node_id=%s reason=%s inputs=%d",
		req.AnalysisID,
		req.NodeID,
		req.Reason,
		len(req.Inputs),
	)
	if r.onSubmitted != nil {
		return r.onSubmitted(ctx, req)
	}
	return nil
}

type persistentDataflowRuntime struct {
	repo               interfaces.AnalysisRepository
	dispatcher         *dagruntime.NodeDispatcher
	projectID          int64
	workflowRepo       interfaces.WorkflowRepository
	dispatchFn         func(ctx context.Context, analysisNodeID int64) error
	onNodeSubmitChange func(nodeID string, delta int)
	buildPersistParams func(req DataflowProcessRunRequest) (*DataflowAnalysisNodePersistParams, bool)
	mu                 sync.Mutex
	inflightDispatches int
	inflightNodeIDs    map[int64]struct{}
}

func (r *persistentDataflowRuntime) SubmitProcessInstance(ctx context.Context, req DataflowProcessRunRequest) error {
	logger.Infof(ctx,
		"[DataflowDagOrchestratorV3] submit process instance, analysis_id=%s node_id=%s reason=%s inputs=%d",
		req.AnalysisID,
		req.NodeID,
		req.Reason,
		len(req.Inputs),
	)

	if r.repo != nil && r.buildPersistParams != nil {
		persistParams, ok := r.buildPersistParams(req)
		if ok {
			node, created, err := r.persistAnalysisNode(ctx, persistParams)
			if err != nil {
				return err
			}
			if created && node != nil {
				dispatch := r.dispatchFn
				if dispatch == nil {
					dispatch = r.dispatcher.Dispatch
				}
				if dispatch == nil {
					return nil
				}
				if err := r.markNodeSubmitted(ctx, node); err != nil {
					return err
				}
				if r.onNodeSubmitChange != nil {
					r.onNodeSubmitChange(strings.TrimSpace(node.NodeID), 1)
				}
				r.incrementInflight(node.ID)
				if err := dispatch(ctx, node.ID); err != nil {
					r.decrementInflight(node.ID)
					if r.onNodeSubmitChange != nil {
						r.onNodeSubmitChange(strings.TrimSpace(node.NodeID), -1)
					}
					if rollbackErr := r.rollbackSubmittedNodeOnDispatchError(ctx, node.ID); rollbackErr != nil {
						logger.Warnf(ctx,
							"[DataflowDagOrchestratorV3] rollback submitted node failed, analysis_id=%s node_id=%s analysis_node_id=%s err=%v",
							req.AnalysisID,
							req.NodeID,
							node.AnalysisNodeID,
							rollbackErr,
						)
					}
					return err
				}
			}
		}
	}
	return nil
}

func (r *persistentDataflowRuntime) markNodeSubmitted(ctx context.Context, node *types.AnalysisNode) error {
	if r == nil || r.repo == nil || node == nil {
		return nil
	}
	status := strings.TrimSpace(strings.ToLower(node.Status))
	if status == "" {
		status = dagruntime.StatusReady
	}
	if err := dagruntime.EnsureTransition(status, dagruntime.StatusSubmitted); err != nil {
		return err
	}
	if err := r.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{"status": dagruntime.StatusSubmitted}); err != nil {
		return err
	}
	node.Status = dagruntime.StatusSubmitted
	return nil
}

func (r *persistentDataflowRuntime) rollbackSubmittedNodeOnDispatchError(ctx context.Context, analysisNodeID int64) error {
	if r == nil || r.repo == nil {
		return nil
	}
	node, err := r.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(strings.ToLower(node.Status)) != dagruntime.StatusSubmitted {
		return nil
	}
	return r.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{"status": dagruntime.StatusReady})
}

func (r *persistentDataflowRuntime) persistAnalysisNode(ctx context.Context, payload *DataflowAnalysisNodePersistParams) (*types.AnalysisNode, bool, error) {
	if r.repo == nil || payload == nil {
		return nil, false, nil
	}
	if payload.AnalysisID <= 0 || strings.TrimSpace(payload.NodeID) == "" {
		return nil, false, nil
	}
	if payload.InputHash == "" {
		payload.InputHash = buildDataflowInstanceInputHash(payload.NodeID, payload.ResolvedInputs, payload.Params)
	}

	existingNodes, err := r.repo.ListAnalysisNodesByAnalysisID(ctx, payload.AnalysisID)
	if err != nil {
		return nil, false, err
	}
	existing := findPersistedNodeInstance(existingNodes, payload.NodeID, payload.InputHash)
	if existing != nil {
		logger.Infof(ctx,
			"[DataflowDagOrchestratorV3] skip persist duplicated analysis node instance, analysis_id=%d node_id=%s input_hash=%s",
			payload.AnalysisID,
			payload.NodeID,
			payload.InputHash,
		)
		return existing, false, nil
	}
	scriptID := strings.TrimSpace(payload.ScriptID)
	script, err := r.workflowRepo.GetScriptByScriptID(ctx, r.projectID, scriptID)
	if err != nil {
		return nil, false, err
	}
	item := buildAnalysisNodeFromPersistPayload(script, payload)
	r.populateNodePathDefaults(ctx, item)
	if err := r.repo.CreateAnalysisNodes(ctx, []*types.AnalysisNode{item}); err != nil {
		return nil, false, err
	}

	logger.Infof(ctx,
		"[DataflowDagOrchestratorV3] persisted analysis node, analysis_id=%d node_id=%s analysis_node_id=%s",
		item.AnalysisID,
		item.NodeID,
		item.AnalysisNodeID,
	)
	return item, true, nil
}

func (r *persistentDataflowRuntime) populateNodePathDefaults(ctx context.Context, node *types.AnalysisNode) {
	if node == nil {
		return
	}

	baseWorkspace := strings.TrimSpace(node.WorkspaceDir)
	if baseWorkspace == "" {
		analysisOutputDir := r.lookupAnalysisOutputDir(ctx, node.AnalysisID)
		if analysisOutputDir != "" {
			baseWorkspace = filepath.Join(analysisOutputDir, fmt.Sprintf("%d", node.ID))
		}
	}

	if strings.TrimSpace(node.WorkspaceDir) == "" {
		node.WorkspaceDir = baseWorkspace
	}
	if strings.TrimSpace(node.OutputDir) == "" && baseWorkspace != "" {
		node.OutputDir = filepath.Join(baseWorkspace, "output")
	}
	if strings.TrimSpace(node.ParamsPath) == "" && baseWorkspace != "" {
		node.ParamsPath = filepath.Join(baseWorkspace, "params.json")
	}
	if strings.TrimSpace(node.CommandPath) == "" && baseWorkspace != "" {
		node.CommandPath = filepath.Join(baseWorkspace, "run.sh")
	}
	if strings.TrimSpace(node.LogPath) == "" && baseWorkspace != "" {
		node.LogPath = filepath.Join(baseWorkspace, "command.log")
	}
}

func (r *persistentDataflowRuntime) lookupAnalysisOutputDir(ctx context.Context, analysisID int64) string {
	if r == nil || r.repo == nil || analysisID <= 0 {
		return ""
	}
	analysis, err := r.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		logger.Warnf(ctx,
			"[DataflowDagOrchestratorV3] lookup analysis output_dir failed, analysis_id=%d err=%v",
			analysisID,
			err,
		)
		return ""
	}
	if analysis == nil {
		return ""
	}
	return strings.TrimSpace(analysis.OutputDir)
}

func findPersistedNodeInstance(items []*types.AnalysisNode, nodeID string, inputHash string) *types.AnalysisNode {
	nodeID = strings.TrimSpace(nodeID)
	inputHash = strings.TrimSpace(inputHash)
	if nodeID == "" || inputHash == "" {
		return nil
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		if strings.TrimSpace(item.NodeID) != nodeID {
			continue
		}
		if strings.TrimSpace(item.InputHash) != inputHash {
			continue
		}
		return item
	}
	return nil
}

func buildDataflowInstanceInputHash(nodeID string, resolvedInputs map[string]any, params map[string]any) string {
	payload := map[string]any{
		"node_id":         strings.TrimSpace(nodeID),
		"resolved_inputs": cloneInputs(resolvedInputs),
		"params":          cloneInputs(params),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		fallback := sha256.Sum256([]byte(strings.TrimSpace(nodeID)))
		return hex.EncodeToString(fallback[:])
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r *persistentDataflowRuntime) incrementInflight(analysisNodeID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if analysisNodeID > 0 {
		if r.inflightNodeIDs == nil {
			r.inflightNodeIDs = make(map[int64]struct{})
		}
		if _, exists := r.inflightNodeIDs[analysisNodeID]; exists {
			return
		}
		r.inflightNodeIDs[analysisNodeID] = struct{}{}
	}
	r.inflightDispatches++
}

func (r *persistentDataflowRuntime) decrementInflight(analysisNodeID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if analysisNodeID > 0 {
		if _, exists := r.inflightNodeIDs[analysisNodeID]; !exists {
			return
		}
		delete(r.inflightNodeIDs, analysisNodeID)
	}
	if r.inflightDispatches > 0 {
		r.inflightDispatches--
	}
}

func (r *persistentDataflowRuntime) onRuntimeEvent(evt dagruntime.RuntimeEvent) {
	name := strings.TrimSpace(evt.Name)
	if name != dagruntime.EventNodeCompleted && name != dagruntime.EventNodeFailed {
		return
	}
	if evt.AnalysisNodeID <= 0 {
		return
	}
	r.decrementInflight(evt.AnalysisNodeID)
}

func (r *persistentDataflowRuntime) InflightDispatches() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inflightDispatches
}

func buildAnalysisNodeFromPersistPayload(script *types.Script, payload *DataflowAnalysisNodePersistParams) *types.AnalysisNode {
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	if status == "" {
		status = "ready"
	}

	return &types.AnalysisNode{
		ID:                     utils.GenerateID(),
		AnalysisNodeID:         "node-" + uuid.NewString(),
		AnalysisID:             payload.AnalysisID,
		NodeID:                 strings.TrimSpace(payload.NodeID),
		NodeName:               strings.TrimSpace(payload.NodeName),
		SampleID:               strings.TrimSpace(payload.SampleID),
		ScriptID:               script.ID,
		InputsPatterns:         dynamicToJSONMap(payload.InputsPatterns),
		ResolvedInputs:         dynamicToJSONMap(payload.ResolvedInputs),
		OutputPatterns:         dynamicToJSONMap(payload.OutputPatterns),
		ResolvedOutputs:        dynamicToJSONMap(payload.ResolvedOutputs),
		Params:                 dynamicToJSONMap(payload.Params),
		InputHash:              strings.TrimSpace(payload.InputHash),
		Status:                 status,
		Executor:               strings.TrimSpace(payload.Executor),
		Retry:                  payload.Retry,
		MaxRetry:               payload.MaxRetry,
		CacheHit:               false,
		UpstreamIDs:            types.JSONSlice(dynamicStringSliceToAny(payload.UpstreamIDs)),
		DownstreamIDs:          types.JSONSlice(dynamicStringSliceToAny(payload.DownstreamIDs)),
		InputValidationErrors:  types.JSONSlice{},
		OutputValidationErrors: types.JSONSlice{},
		RerunReason:            strings.TrimSpace(payload.RerunReason),
		WorkspaceDir:           strings.TrimSpace(payload.WorkspaceDir),
		OutputDir:              strings.TrimSpace(payload.OutputDir),
		CommandPath:            strings.TrimSpace(payload.CommandPath),
		ParamsPath:             strings.TrimSpace(payload.ParamsPath),
		LogPath:                strings.TrimSpace(payload.LogPath),
		CreationSource:         "scheduler",
	}
}

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

func (k *dataflowKernel) buildAnalysisNodePersistParams(req DataflowProcessRunRequest) (*DataflowAnalysisNodePersistParams, bool) {
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

	params := cloneInputs(proc.Params)
	resolvedInputs := cloneInputs(proc.ResolvedIn)
	for key, value := range req.Inputs {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		params[trimmed] = value
		resolvedInputs[trimmed] = value
	}

	return &DataflowAnalysisNodePersistParams{
		AnalysisID:      analysisID,
		NodeID:          nodeID,
		InputHash:       buildDataflowInstanceInputHash(nodeID, resolvedInputs, params),
		NodeName:        strings.TrimSpace(proc.NodeName),
		SampleID:        strings.TrimSpace(proc.SampleID),
		ScriptID:        strings.TrimSpace(proc.ScriptID),
		InputsPatterns:  cloneInputs(proc.Inputs),
		OutputPatterns:  cloneInputs(proc.Outputs),
		Params:          params,
		ResolvedInputs:  resolvedInputs,
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
	k.markNodeCompleted(nodeID)
	outputs := map[string]any(node.ResolvedOutputs)
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

func (o *dataflowDagOrchestratorV3) StartAsyncV3(ctx context.Context, projectID int64, analysisID int64, parseAnalysisResult map[string]any, dagDefinition map[string]any) error {
	if analysisID <= 0 {
		return fmt.Errorf("analysis_id is required")
	}

	bgCtx := context.Background()

	go func() {
		if err := o.runStartAsyncV3(bgCtx, projectID, analysisID, parseAnalysisResult, dagDefinition); err != nil {
			logger.Errorf(bgCtx, "[DataflowDagOrchestratorV3] async run failed, analysis_id=%d err=%v", analysisID, err)
		}
	}()

	return nil
}

func (o *dataflowDagOrchestratorV3) runStartAsyncV3(ctx context.Context, projectID int64, analysisID int64, parseAnalysisResult map[string]any, dagDefinition map[string]any) error {
	if err := o.prepareAnalysisByCacheTypeV3(ctx, analysisID); err != nil {
		return fmt.Errorf("prepare analysis by cache_type failed: %w", err)
	}

	spec := o.buildGraphSpec(analysisID, dagDefinition)
	o.logFrameworkPhase(ctx, spec)

	// runtimeEngine := dagruntime.NewRuntimeEngine(o.repo)
	// storageBase := ""
	// if o.cfg != nil && o.cfg.Storage != nil {
	// 	storageBase = strings.TrimSpace(o.cfg.Storage.BaseDir)
	// }
	// preparer := dagruntime.NewFileSystemNodeRuntimePreparerWithBuilders(o.repo, o.workflowRepo, o.projectRepo, storageBase, o.runScriptBuilders)
	// dispatcher := dagruntime.NewNodeDispatcher(
	// 	runtimeEngine,
	// 	o.repo,
	// 	o.bus,
	// 	executor.NewFactory(executor.FactoryDeps{
	// 		WorkflowRepository: o.workflowRepo,
	// 		ContainerManager:   o.containerMgr,
	// 	}),
	// 	nil,
	// 	preparer,
	// )

	runtimeEvents := make(chan dagruntime.RuntimeEvent, 256)
	if o.bus != nil {
		o.bus.Subscribe(&dataflowRuntimeEventHandler{analysisID: analysisID, events: runtimeEvents})
	}

	var kernel *dataflowKernel
	runtime := &persistentDataflowRuntime{
		repo:       o.repo,
		dispatcher: o.dispatcher,
		onNodeSubmitChange: func(nodeID string, delta int) {
			if kernel == nil {
				return
			}
			kernel.adjustSubmittedCount(nodeID, delta)
		},
		buildPersistParams: func(req DataflowProcessRunRequest) (*DataflowAnalysisNodePersistParams, bool) {
			if kernel == nil {
				return nil, false
			}
			return kernel.buildAnalysisNodePersistParams(req)
		}, workflowRepo: o.workflowRepo,
		projectID: projectID,
	}
	kernel = newDataflowKernel(spec, runtime, parseAnalysisResult)
	kernel.repo = o.repo
	if err := kernel.bootstrapSourceProcesses(ctx); err != nil {
		return err
	}
	// if err := kernel.closeAll(ctx); err != nil {
	// 	return err
	// }

	idleWindow := 200 * time.Millisecond
	idleTimer := time.NewTimer(idleWindow)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt := <-runtimeEvents:
			runtime.onRuntimeEvent(evt)
			eventName := strings.TrimSpace(evt.Name)
			switch eventName {
			case dagruntime.EventNodeCompleted:
				if err := kernel.onNodeCompleted(ctx, evt.AnalysisNodeID); err != nil {
					return err
				}
			case dagruntime.EventNodeFailed:
				if err := kernel.onNodeFailed(ctx, evt.AnalysisNodeID); err != nil {
					return err
				}
			default:
				continue
			}
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleWindow)
		case <-idleTimer.C:
			inflight := runtime.InflightDispatches()
			if inflight == 0 {
				select {
				case evt := <-runtimeEvents:
					runtime.onRuntimeEvent(evt)
					eventName := strings.TrimSpace(evt.Name)
					if eventName == dagruntime.EventNodeCompleted {
						if err := kernel.onNodeCompleted(ctx, evt.AnalysisNodeID); err != nil {
							return err
						}
					} else if eventName == dagruntime.EventNodeFailed {
						if err := kernel.onNodeFailed(ctx, evt.AnalysisNodeID); err != nil {
							return err
						}
					}
					idleTimer.Reset(idleWindow)
				default:
					if kernel.checkFinished(inflight) {
						return nil
					}
					idleTimer.Reset(idleWindow)
				}
				continue
			}
			idleTimer.Reset(idleWindow)
		}
	}
}

// prepareAnalysisByCacheTypeV3 handles cache-type driven pre-run behavior.
//
// Current V3 scope:
// - CacheTypeRerunAll: clear persisted runtime graph and rebuild from scratch.
// - CacheTypeReuseExistingNode: keep persisted runtime graph for reuse.
func (o *dataflowDagOrchestratorV3) prepareAnalysisByCacheTypeV3(ctx context.Context, analysisID int64) error {
	if analysisID <= 0 {
		return nil
	}

	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return err
	}
	if analysis == nil {
		return nil
	}

	switch analysis.CacheType {
	case types.CacheTypeRerunAll:
		if err := o.repo.WithTransaction(ctx, func(tx interfaces.AnalysisRepository) error {
			if err := tx.DeleteAnalysisNodesByAnalysisID(ctx, analysisID); err != nil {
				return err
			}
			if err := tx.DeleteAnalysisEdgesByAnalysisID(ctx, analysisID); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		logger.Infof(ctx,
			"[DataflowDagOrchestratorV3] cache_type rerun_all, cleared persisted graph, analysis_id=%d",
			analysisID,
		)
	case types.CacheTypeReuseExistingNode:
		logger.Infof(ctx,
			"[DataflowDagOrchestratorV3] cache_type reuse_existing_node, keep persisted graph, analysis_id=%d",
			analysisID,
		)
	default:
		// Keep existing behavior for other cache types in this incremental step.
	}

	return nil
}

func (o *dataflowDagOrchestratorV3) buildGraphSpec(analysisID int64, dagDefinition map[string]any) DataflowGraphSpec {
	spec := DataflowGraphSpec{AnalysisID: analysisID}

	edges := dynamicToMapSlice(dagDefinition["edges"])
	for _, edge := range edges {
		channelID := dataflowChannelID(
			strings.TrimSpace(dynamicToString(edge["source"])),
			strings.TrimSpace(dynamicToString(edge["sourceHandle"])),
			strings.TrimSpace(dynamicToString(edge["target"])),
			strings.TrimSpace(dynamicToString(edge["targetHandle"])),
		)
		spec.Channels = append(spec.Channels, DataflowChannelSpec{
			ChannelID:  channelID,
			FromNodeID: strings.TrimSpace(dynamicToString(edge["source"])),
			ToNodeID:   strings.TrimSpace(dynamicToString(edge["target"])),
			FromPort:   strings.TrimSpace(dynamicToString(edge["sourceHandle"])),
			ToPort:     strings.TrimSpace(dynamicToString(edge["targetHandle"])),
		})
	}

	nodeByID := map[string]*DataflowProcessSpec{}
	nodes := dynamicToMapSlice(dagDefinition["nodes"])
	for _, node := range nodes {
		nodeID := strings.TrimSpace(dynamicToString(node["id"]))
		if nodeID == "" {
			continue
		}
		opType, scatterField, scatterMode, gatherField, gatherMode := resolveDataflowOperatorConfig(node)
		inputKeys := extractNodeInputKeys(node)
		if _, exists := nodeByID[nodeID]; !exists {
			nodeName := strings.TrimSpace(dynamicToString(resolveNodeField(node, "node_name")))
			if nodeName == "" {
				nodeName = strings.TrimSpace(dynamicToString(resolveNodeField(node, "name")))
			}
			nodeByID[nodeID] = &DataflowProcessSpec{
				NodeID:       nodeID,
				NodeName:     nodeName,
				SampleID:     strings.TrimSpace(dynamicToString(resolveNodeField(node, "sample_id"))),
				ScriptID:     strings.TrimSpace(dynamicToString(resolveNodeField(node, "script_id"))),
				InputKeys:    inputKeys,
				Inputs:       dynamicToMap(resolveNodeField(node, "inputs")),
				Outputs:      dynamicToMap(resolveNodeField(node, "outputs")),
				Params:       dynamicToMap(resolveNodeField(node, "params")),
				ResolvedIn:   dynamicToMap(resolveNodeField(node, "resolved_inputs")),
				ResolvedOut:  dynamicToMap(resolveNodeField(node, "resolved_outputs")),
				Executor:     strings.TrimSpace(dynamicToString(resolveNodeField(node, "executor"))),
				Retry:        dynamicIntFromAny(resolveNodeField(node, "retry"), 0),
				MaxRetry:     dynamicIntFromAny(resolveNodeField(node, "max_retry"), 3),
				RerunReason:  strings.TrimSpace(dynamicToString(resolveNodeField(node, "rerun_reason"))),
				OperatorType: opType,
				ScatterField: scatterField,
				ScatterMode:  scatterMode,
				GatherField:  gatherField,
				GatherMode:   gatherMode,
			}
		}
	}
	for _, ch := range spec.Channels {
		if ch.FromNodeID == "" || ch.ToNodeID == "" {
			continue
		}
		up, ok := nodeByID[ch.ToNodeID]
		if ok {
			up.UpstreamIDs = appendUniqueString(up.UpstreamIDs, ch.FromNodeID)
		}
		down, ok := nodeByID[ch.FromNodeID]
		if ok {
			down.Downstream = appendUniqueString(down.Downstream, ch.ToNodeID)
		}
	}
	for _, proc := range nodeByID {
		spec.Processes = append(spec.Processes, *proc)
	}
	return spec
}

func (o *dataflowDagOrchestratorV3) logFrameworkPhase(ctx context.Context, spec DataflowGraphSpec) {
	logger.Infof(ctx,
		"[DataflowDagOrchestratorV3] framework bootstrap, analysis_id=%s processes=%d channels=%d",
		spec.AnalysisID,
		len(spec.Processes),
		len(spec.Channels),
	)
}

func appendUniqueString(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if strings.TrimSpace(item) == value {
			return items
		}
	}
	return append(items, value)
}

func resolveDataflowOperatorConfig(node map[string]any) (operatorType string, scatterField string, scatterMode string, gatherField string, gatherMode string) {
	gather := dynamicToMap(node["gather"])
	gatherMode = strings.ToLower(strings.TrimSpace(dynamicToString(gather["mode"])))
	gatherField = strings.TrimSpace(dynamicToString(gather["field"]))
	if gatherMode == "list" && gatherField != "" {
		return string(dataflowOperatorTypeGather), "", "", gatherField, gatherMode
	}

	scatter := dynamicToMap(node["scatter"])
	scatterMode = strings.ToLower(strings.TrimSpace(dynamicToString(scatter["mode"])))
	scatterField = strings.TrimSpace(dynamicToString(scatter["field"]))
	if (scatterMode == "each" || scatterMode == "list") && scatterField != "" {
		return string(dataflowOperatorTypeScatter), scatterField, scatterMode, "", ""
	}

	return string(dataflowOperatorTypeInput), "", "", "", ""
}

func dynamicToMap(value any) map[string]any {
	m, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(m))
	for key, item := range m {
		cloned[key] = item
	}
	return cloned
}

func resolveNodeField(node map[string]any, key string) any {
	if node == nil {
		return nil
	}
	if value, ok := node[key]; ok {
		return value
	}
	if data := dynamicToMap(node["data"]); len(data) > 0 {
		if value, ok := data[key]; ok {
			return value
		}
	}
	return nil
}

func toAnySlice(value any) ([]any, bool) {
	if value == nil {
		return nil, false
	}
	if items, ok := value.([]any); ok {
		copied := make([]any, len(items))
		copy(copied, items)
		return copied, true
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Array && rv.Kind() != reflect.Slice {
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

func cloneInputs(inputs map[string]any) map[string]any {
	cloned := make(map[string]any, len(inputs))
	for key, value := range inputs {
		cloned[key] = value
	}
	return cloned
}

func extractNodeInputKeys(node map[string]any) []string {
	inputs := dynamicToMap(node["inputs"])
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = appendUniqueString(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func dataflowChannelID(fromNodeID string, fromPort string, toNodeID string, toPort string) string {
	return fmt.Sprintf("%s:%s->%s:%s", strings.TrimSpace(fromNodeID), strings.TrimSpace(fromPort), strings.TrimSpace(toNodeID), strings.TrimSpace(toPort))
}

func dataflowSourceChannelID(nodeID string, inputKey string) string {
	return fmt.Sprintf("source:%s:%s", strings.TrimSpace(nodeID), strings.TrimSpace(inputKey))
}
