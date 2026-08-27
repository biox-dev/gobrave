package service

// import (
// 	"context"
// 	"encoding/json"
// 	"fmt"
// 	"strings"
// 	"sync"
// 	"testing"
// 	"time"

// 	dagruntime "github.com/biox-dev/gobrave/internal/dag"
// 	"github.com/biox-dev/gobrave/internal/event"
// 	"github.com/biox-dev/gobrave/internal/types"
// 	"github.com/biox-dev/gobrave/internal/types/interfaces"
// 	"github.com/biox-dev/gobrave/internal/utils"
// )

// var testSnowflakeInitOnce sync.Once

// func ensureTestSnowflake(t *testing.T) {
// 	t.Helper()
// 	testSnowflakeInitOnce.Do(func() {
// 		if err := utils.InitSnowflake(1); err != nil {
// 			t.Fatalf("init snowflake failed: %v", err)
// 		}
// 	})
// }

// type captureDataflowRuntime struct {
// 	mu    sync.Mutex
// 	items []DataflowProcessRunRequest
// }

// type feedbackCaptureDataflowRuntime struct {
// 	mu          sync.Mutex
// 	items       []DataflowProcessRunRequest
// 	onSubmitted func(context.Context, DataflowProcessRunRequest) error
// }

// func (r *captureDataflowRuntime) SubmitProcessInstance(_ context.Context, req DataflowProcessRunRequest) error {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	r.items = append(r.items, req)
// 	return nil
// }

// func (r *captureDataflowRuntime) Requests() []DataflowProcessRunRequest {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	out := make([]DataflowProcessRunRequest, len(r.items))
// 	copy(out, r.items)
// 	return out
// }

// func (r *feedbackCaptureDataflowRuntime) SubmitProcessInstance(ctx context.Context, req DataflowProcessRunRequest) error {
// 	r.mu.Lock()
// 	r.items = append(r.items, req)
// 	r.mu.Unlock()
// 	if r.onSubmitted != nil {
// 		return r.onSubmitted(ctx, req)
// 	}
// 	return nil
// }

// func (r *feedbackCaptureDataflowRuntime) Requests() []DataflowProcessRunRequest {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	out := make([]DataflowProcessRunRequest, len(r.items))
// 	copy(out, r.items)
// 	return out
// }

// func TestInputOperator(t *testing.T) {
// 	ctx := context.Background()
// 	runtime := &captureDataflowRuntime{}
// 	op := newInputOperator("analysis-1", "node-input", map[string]string{
// 		"ch-fastq1": "fastq1",
// 		"ch-fastq2": "fastq2",
// 	}, map[string]*DataflowChannel{}, runtime)

// 	if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-fastq1", Value: "r1_a"}); err != nil {
// 		t.Fatalf("notify fastq1 #1 failed: %v", err)
// 	}
// 	if got := len(runtime.Requests()); got != 0 {
// 		t.Fatalf("expected no submission before all inputs are ready, got=%d", got)
// 	}

// 	if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-fastq2", Value: "r2_a"}); err != nil {
// 		t.Fatalf("notify fastq2 #1 failed: %v", err)
// 	}
// 	if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-fastq1", Value: "r1_b"}); err != nil {
// 		t.Fatalf("notify fastq1 #2 failed: %v", err)
// 	}
// 	if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-fastq2", Value: "r2_b"}); err != nil {
// 		t.Fatalf("notify fastq2 #2 failed: %v", err)
// 	}

// 	reqs := runtime.Requests()
// 	if len(reqs) != 2 {
// 		t.Fatalf("expected 2 submissions, got=%d", len(reqs))
// 	}

// 	if reqs[0].Reason != "input-ready" {
// 		t.Fatalf("unexpected first reason: got=%q want=%q", reqs[0].Reason, "input-ready")
// 	}
// 	if reqs[0].Inputs["fastq1"] != "r1_a" || reqs[0].Inputs["fastq2"] != "r2_a" {
// 		t.Fatalf("unexpected first inputs: got=%v", reqs[0].Inputs)
// 	}

// 	if reqs[1].Reason != "input-ready" {
// 		t.Fatalf("unexpected second reason: got=%q want=%q", reqs[1].Reason, "input-ready")
// 	}
// 	if reqs[1].Inputs["fastq1"] != "r1_b" || reqs[1].Inputs["fastq2"] != "r2_b" {
// 		t.Fatalf("unexpected second inputs: got=%v", reqs[1].Inputs)
// 	}
// }

// func TestScatterOperator(t *testing.T) {
// 	t.Run("each mode expands list to multiple submissions", func(t *testing.T) {
// 		ctx := context.Background()
// 		runtime := &captureDataflowRuntime{}
// 		op := newScatterOperator(
// 			"analysis-1",
// 			"node-scatter-each",
// 			map[string]string{"ch-reads": "reads"},
// 			map[string]*DataflowChannel{},
// 			"reads",
// 			"each",
// 			runtime,
// 		)

// 		if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-reads", Value: []any{"s1", "s2"}}); err != nil {
// 			t.Fatalf("notify failed: %v", err)
// 		}

// 		reqs := runtime.Requests()
// 		if len(reqs) != 2 {
// 			t.Fatalf("expected 2 submissions, got=%d", len(reqs))
// 		}
// 		if reqs[0].Reason != "scatter-each-ready" || reqs[1].Reason != "scatter-each-ready" {
// 			t.Fatalf("unexpected reasons: got=%q,%q", reqs[0].Reason, reqs[1].Reason)
// 		}
// 		if reqs[0].Inputs["reads"] != "s1" || reqs[1].Inputs["reads"] != "s2" {
// 			t.Fatalf("unexpected expanded inputs: got=%v / %v", reqs[0].Inputs, reqs[1].Inputs)
// 		}
// 	})

// 	t.Run("list mode normalizes to a list and submits once", func(t *testing.T) {
// 		ctx := context.Background()
// 		runtime := &captureDataflowRuntime{}
// 		op := newScatterOperator(
// 			"analysis-1",
// 			"node-scatter-list",
// 			map[string]string{"ch-reads": "reads"},
// 			map[string]*DataflowChannel{},
// 			"reads",
// 			"list",
// 			runtime,
// 		)

// 		if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-reads", Value: "only-one"}); err != nil {
// 			t.Fatalf("notify failed: %v", err)
// 		}

// 		reqs := runtime.Requests()
// 		if len(reqs) != 1 {
// 			t.Fatalf("expected 1 submission, got=%d", len(reqs))
// 		}
// 		if reqs[0].Reason != "scatter-list-ready" {
// 			t.Fatalf("unexpected reason: got=%q want=%q", reqs[0].Reason, "scatter-list-ready")
// 		}
// 		reads, ok := reqs[0].Inputs["reads"].([]any)
// 		if !ok {
// 			t.Fatalf("reads should be []any, got=%T", reqs[0].Inputs["reads"])
// 		}
// 		if len(reads) != 1 || reads[0] != "only-one" {
// 			t.Fatalf("unexpected reads payload: got=%v", reads)
// 		}
// 	})
// }

// func TestGatherOperator(t *testing.T) {
// 	ctx := context.Background()
// 	runtime := &captureDataflowRuntime{}
// 	op := newGatherOperator(
// 		"analysis-1",
// 		"node-gather",
// 		map[string]string{
// 			"ch-bam":   "bam",
// 			"ch-index": "index",
// 		},
// 		map[string]*DataflowChannel{},
// 		"bam",
// 		"list",
// 		runtime,
// 	)

// 	if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-bam", Value: "b1.bam"}); err != nil {
// 		t.Fatalf("notify bam #1 failed: %v", err)
// 	}
// 	if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-bam", Value: "b2.bam"}); err != nil {
// 		t.Fatalf("notify bam #2 failed: %v", err)
// 	}
// 	if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-index", Value: "/ref/index"}); err != nil {
// 		t.Fatalf("notify index failed: %v", err)
// 	}

// 	if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-bam", Closed: true}); err != nil {
// 		t.Fatalf("close bam failed: %v", err)
// 	}
// 	if got := len(runtime.Requests()); got != 0 {
// 		t.Fatalf("expected no submission before all channels close, got=%d", got)
// 	}

// 	if err := op.Notify(ctx, DataflowSignal{ChannelID: "ch-index", Closed: true}); err != nil {
// 		t.Fatalf("close index failed: %v", err)
// 	}

// 	reqs := runtime.Requests()
// 	if len(reqs) != 1 {
// 		t.Fatalf("expected 1 submission, got=%d", len(reqs))
// 	}
// 	if reqs[0].Reason != "gather-list-ready" {
// 		t.Fatalf("unexpected reason: got=%q want=%q", reqs[0].Reason, "gather-list-ready")
// 	}
// 	bams, ok := reqs[0].Inputs["bam"].([]any)
// 	if !ok {
// 		t.Fatalf("bam should be []any, got=%T", reqs[0].Inputs["bam"])
// 	}
// 	if len(bams) != 2 || bams[0] != "b1.bam" || bams[1] != "b2.bam" {
// 		t.Fatalf("unexpected bam list: got=%v", bams)
// 	}
// 	if reqs[0].Inputs["index"] != "/ref/index" {
// 		t.Fatalf("unexpected index input: got=%v", reqs[0].Inputs["index"])
// 	}
// }

// func TestBootstrapSourceProcesses(t *testing.T) {
// 	t.Run("source scatter each from params", func(t *testing.T) {
// 		ctx := context.Background()
// 		runtime := &captureDataflowRuntime{}
// 		k := &dataflowKernel{
// 			analysisID: "analysis-1",
// 			runtime:    runtime,
// 			params: map[string]any{
// 				"reads": []any{"s1", "s2"},
// 			},
// 			sourceNodes: []DataflowProcessSpec{
// 				{
// 					NodeID:       "node-fastp",
// 					OperatorType: "scatter",
// 					ScatterField: "reads",
// 					ScatterMode:  "each",
// 				},
// 			},
// 		}

// 		if err := k.bootstrapSourceProcesses(ctx); err != nil {
// 			t.Fatalf("bootstrap failed: %v", err)
// 		}

// 		reqs := runtime.Requests()
// 		if len(reqs) != 2 {
// 			t.Fatalf("expected 2 submissions, got=%d", len(reqs))
// 		}
// 		if reqs[0].Reason != "scatter-each-ready" || reqs[1].Reason != "scatter-each-ready" {
// 			t.Fatalf("unexpected reasons: %q, %q", reqs[0].Reason, reqs[1].Reason)
// 		}
// 		if reqs[0].Inputs["reads"] != "s1" || reqs[1].Inputs["reads"] != "s2" {
// 			t.Fatalf("unexpected scatter values: %v / %v", reqs[0].Inputs["reads"], reqs[1].Inputs["reads"])
// 		}
// 	})

// 	t.Run("source scatter uses scatter field list from params even when input keys differ", func(t *testing.T) {
// 		ctx := context.Background()
// 		runtime := &captureDataflowRuntime{}
// 		k := &dataflowKernel{
// 			analysisID: "analysis-1",
// 			runtime:    runtime,
// 			params: map[string]any{
// 				"reads": []any{
// 					map[string]any{"FASTQ_R1": "/data/test1_1.fq", "FASTQ_R2": "/data/test1_2.fq"},
// 					map[string]any{"FASTQ_R1": "/data/test2_1.fq", "FASTQ_R2": "/data/test2_2.fq"},
// 				},
// 			},
// 			sourceNodes: []DataflowProcessSpec{
// 				{
// 					NodeID:       "node-fastp",
// 					InputKeys:    []string{"fastq1", "fastq2"},
// 					OperatorType: "scatter",
// 					ScatterField: "reads",
// 					ScatterMode:  "each",
// 				},
// 			},
// 		}

// 		if err := k.bootstrapSourceProcesses(ctx); err != nil {
// 			t.Fatalf("bootstrap failed: %v", err)
// 		}

// 		reqs := runtime.Requests()
// 		if len(reqs) != 2 {
// 			t.Fatalf("expected 2 submissions, got=%d", len(reqs))
// 		}
// 		for i := range reqs {
// 			if reqs[i].Reason != "scatter-each-ready" {
// 				t.Fatalf("unexpected reason: got=%q want=%q", reqs[i].Reason, "scatter-each-ready")
// 			}
// 			if _, ok := reqs[i].Inputs["reads"]; !ok {
// 				t.Fatalf("reads input must exist, got=%v", reqs[i].Inputs)
// 			}
// 			if _, hasFastq1 := reqs[i].Inputs["fastq1"]; hasFastq1 {
// 				t.Fatalf("fastq1 should not be sourced directly from input keys: got=%v", reqs[i].Inputs)
// 			}
// 			if _, hasFastq2 := reqs[i].Inputs["fastq2"]; hasFastq2 {
// 				t.Fatalf("fastq2 should not be sourced directly from input keys: got=%v", reqs[i].Inputs)
// 			}
// 		}
// 	})

// 	t.Run("source direct input from params", func(t *testing.T) {
// 		ctx := context.Background()
// 		runtime := &captureDataflowRuntime{}
// 		k := &dataflowKernel{
// 			analysisID: "analysis-1",
// 			runtime:    runtime,
// 			params: map[string]any{
// 				"reference_genome": "/data/index",
// 				"reads":            []any{"ignored"},
// 			},
// 			sourceNodes: []DataflowProcessSpec{
// 				{
// 					NodeID:       "node-bwa-index",
// 					InputKeys:    []string{"reference_genome"},
// 					OperatorType: "input",
// 				},
// 			},
// 		}

// 		if err := k.bootstrapSourceProcesses(ctx); err != nil {
// 			t.Fatalf("bootstrap failed: %v", err)
// 		}

// 		reqs := runtime.Requests()
// 		if len(reqs) != 1 {
// 			t.Fatalf("expected 1 submission, got=%d", len(reqs))
// 		}
// 		if reqs[0].Reason != "input-ready" {
// 			t.Fatalf("unexpected reason: got=%q want=%q", reqs[0].Reason, "input-ready")
// 		}
// 		if reqs[0].Inputs["reference_genome"] != "/data/index" {
// 			t.Fatalf("unexpected input: got=%v", reqs[0].Inputs)
// 		}
// 		if _, exists := reqs[0].Inputs["reads"]; exists {
// 			t.Fatalf("reads should not be injected for direct node inputs: got=%v", reqs[0].Inputs)
// 		}
// 	})

// }

// func TestBuildAnalysisNodePersistParams(t *testing.T) {
// 	k := &dataflowKernel{
// 		analysisID: "analysis-1",
// 		processByNode: map[string]DataflowProcessSpec{
// 			"node-align": {
// 				NodeID:      "node-align",
// 				NodeName:    "BWA Align",
// 				SampleID:    "sample-a",
// 				ScriptID:    "script-bwa",
// 				Inputs:      map[string]any{"reads": map[string]any{"required": true}},
// 				Outputs:     map[string]any{"bam": map[string]any{"required": true}},
// 				Params:      map[string]any{"threads": 8},
// 				ResolvedIn:  map[string]any{"ref": "/ref/hg38.fa"},
// 				ResolvedOut: map[string]any{},
// 				UpstreamIDs: []string{"node-fastp"},
// 				Downstream:  []string{"node-sort"},
// 				Executor:    "docker",
// 				Retry:       0,
// 				MaxRetry:    3,
// 			},
// 		},
// 	}

// 	payload, ok := k.buildAnalysisNodePersistParams(DataflowProcessRunRequest{
// 		AnalysisID: "analysis-1",
// 		NodeID:     "node-align",
// 		Inputs: map[string]any{
// 			"reads": []any{"r1.fq", "r2.fq"},
// 		},
// 		Reason: "input-ready",
// 	})
// 	if !ok {
// 		t.Fatalf("expected assembled payload")
// 	}
// 	if payload.AnalysisID != "analysis-1" {
// 		t.Fatalf("unexpected analysis id: got=%q", payload.AnalysisID)
// 	}
// 	if payload.NodeID != "node-align" || payload.ScriptID != "script-bwa" {
// 		t.Fatalf("unexpected node identifiers: got node=%q script=%q", payload.NodeID, payload.ScriptID)
// 	}
// 	if payload.Params["threads"] != 8 {
// 		t.Fatalf("template params should be preserved: got=%v", payload.Params)
// 	}
// 	if _, ok := payload.Params["reads"]; !ok {
// 		t.Fatalf("request inputs should be merged into params: got=%v", payload.Params)
// 	}
// 	if payload.ResolvedInputs["ref"] != "/ref/hg38.fa" {
// 		t.Fatalf("resolved input template should be preserved: got=%v", payload.ResolvedInputs)
// 	}
// 	if payload.SubmitReason != "input-ready" {
// 		t.Fatalf("unexpected submit reason: got=%q", payload.SubmitReason)
// 	}
// 	if payload.Status != "ready" {
// 		t.Fatalf("unexpected default status: got=%q", payload.Status)
// 	}
// 	if payload.InputHash == "" {
// 		t.Fatalf("input hash should be generated for instance-level idempotency")
// 	}
// }

// func TestBuildDataflowInstanceInputHashStable(t *testing.T) {
// 	hashA := buildDataflowInstanceInputHash(
// 		"node-align",
// 		map[string]any{"a": 1, "b": 2},
// 		map[string]any{"threads": 8},
// 	)
// 	hashB := buildDataflowInstanceInputHash(
// 		"node-align",
// 		map[string]any{"b": 2, "a": 1},
// 		map[string]any{"threads": 8},
// 	)
// 	hashC := buildDataflowInstanceInputHash(
// 		"node-align",
// 		map[string]any{"a": 1, "b": 3},
// 		map[string]any{"threads": 8},
// 	)

// 	if hashA == "" || hashB == "" || hashC == "" {
// 		t.Fatalf("hash should never be empty")
// 	}
// 	if hashA != hashB {
// 		t.Fatalf("hash should be stable across map key order: %q != %q", hashA, hashB)
// 	}
// 	if hashA == hashC {
// 		t.Fatalf("hash should change when inputs change: %q", hashA)
// 	}
// }

// func TestFindPersistedNodeInstance(t *testing.T) {
// 	items := []*types.AnalysisNode{
// 		{AnalysisID: "analysis-1", NodeID: "node-a", InputHash: "hash-1", AnalysisNodeID: "node-1"},
// 		{AnalysisID: "analysis-1", NodeID: "node-a", InputHash: "hash-2", AnalysisNodeID: "node-2"},
// 		{AnalysisID: "analysis-1", NodeID: "node-b", InputHash: "hash-1", AnalysisNodeID: "node-3"},
// 	}

// 	matched := findPersistedNodeInstance(items, "node-a", "hash-2")
// 	if matched == nil {
// 		t.Fatalf("expected matched instance")
// 	}
// 	if matched.AnalysisNodeID != "node-2" {
// 		t.Fatalf("unexpected matched node id: got=%q", matched.AnalysisNodeID)
// 	}

// 	notFound := findPersistedNodeInstance(items, "node-a", "hash-3")
// 	if notFound != nil {
// 		t.Fatalf("should not match non-existing hash")
// 	}
// }

// func TestKernelSubmissionFeedsDownstreamChannels(t *testing.T) {
// 	ctx := context.Background()
// 	spec := DataflowGraphSpec{
// 		AnalysisID: "analysis-1",
// 		Processes: []DataflowProcessSpec{
// 			{
// 				NodeID:       "node-a",
// 				InputKeys:    []string{"reads"},
// 				OperatorType: "input",
// 			},
// 			{
// 				NodeID:       "node-b",
// 				OperatorType: "input",
// 			},
// 		},
// 		Channels: []DataflowChannelSpec{
// 			{
// 				ChannelID:  dataflowChannelID("node-a", "out", "node-b", "in"),
// 				FromNodeID: "node-a",
// 				ToNodeID:   "node-b",
// 				FromPort:   "out",
// 				ToPort:     "in",
// 			},
// 		},
// 	}

// 	runtime := &captureDataflowRuntime{}
// 	k := newDataflowKernel(spec, runtime, map[string]any{"reads": "r1.fq"})
// 	if err := k.emitToDownstream(ctx, "node-a", map[string]any{"out": "bwa.bam"}); err != nil {
// 		t.Fatalf("emitToDownstream failed: %v", err)
// 	}

// 	reqs := runtime.Requests()
// 	if len(reqs) != 1 {
// 		t.Fatalf("expected 1 downstream submission, got=%d", len(reqs))
// 	}
// 	if reqs[0].NodeID != "node-b" {
// 		t.Fatalf("submission should be downstream node-b, got=%q", reqs[0].NodeID)
// 	}
// 	if reqs[0].Inputs["in"] != "bwa.bam" {
// 		t.Fatalf("downstream input should receive fromPort value, got=%v", reqs[0].Inputs)
// 	}
// }

// type syncEventBus struct {
// 	mu       sync.RWMutex
// 	handlers []event.Handler
// }

// func newSyncEventBus() *syncEventBus {
// 	return &syncEventBus{}
// }

// func (b *syncEventBus) Publish(evt event.Event) {
// 	b.mu.RLock()
// 	handlers := append([]event.Handler(nil), b.handlers...)
// 	b.mu.RUnlock()
// 	for _, h := range handlers {
// 		h.Handle(evt)
// 	}
// }

// func (b *syncEventBus) Subscribe(handler event.Handler) {
// 	if handler == nil {
// 		return
// 	}
// 	b.mu.Lock()
// 	defer b.mu.Unlock()
// 	b.handlers = append(b.handlers, handler)
// }

// type inMemoryAnalysisRepo struct {
// 	mu            sync.Mutex
// 	analyses      map[string]*types.Analysis
// 	nodesByID     map[int64]*types.AnalysisNode
// 	nodesByAID    map[string][]*types.AnalysisNode
// 	nextNodeIDSeq int64
// }

// var _ interfaces.AnalysisRepository = (*inMemoryAnalysisRepo)(nil)

// func newInMemoryAnalysisRepo() *inMemoryAnalysisRepo {
// 	return &inMemoryAnalysisRepo{
// 		analyses:      map[string]*types.Analysis{},
// 		nodesByID:     map[int64]*types.AnalysisNode{},
// 		nodesByAID:    map[string][]*types.AnalysisNode{},
// 		nextNodeIDSeq: 100,
// 	}
// }

// func (r *inMemoryAnalysisRepo) GetAnalysisByAnalysisID(_ context.Context, analysisID string) (*types.Analysis, error) {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	item, ok := r.analyses[strings.TrimSpace(analysisID)]
// 	if !ok {
// 		return nil, nil
// 	}
// 	out := *item
// 	return &out, nil
// }

// func (r *inMemoryAnalysisRepo) ListAnalysisByJobStatus(_ context.Context, _ string) ([]*types.Analysis, error) {
// 	return nil, nil
// }

// func (r *inMemoryAnalysisRepo) GetAnalysisNodeByID(_ context.Context, id int64) (*types.AnalysisNode, error) {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	node, ok := r.nodesByID[id]
// 	if !ok {
// 		return nil, nil
// 	}
// 	out := *node
// 	return &out, nil
// }

// func (r *inMemoryAnalysisRepo) GetAnalysisNodeByAnalysisNodeID(_ context.Context, analysisNodeID string) (*types.AnalysisNode, error) {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	needle := strings.TrimSpace(analysisNodeID)
// 	for _, node := range r.nodesByID {
// 		if node != nil && strings.TrimSpace(node.AnalysisNodeID) == needle {
// 			out := *node
// 			return &out, nil
// 		}
// 	}
// 	return nil, nil
// }

// func (r *inMemoryAnalysisRepo) GetAnalysisNodeByNodeID(_ context.Context, analysisID string, nodeID string) (*types.AnalysisNode, error) {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	for _, node := range r.nodesByAID[strings.TrimSpace(analysisID)] {
// 		if node != nil && strings.TrimSpace(node.NodeID) == strings.TrimSpace(nodeID) {
// 			out := *node
// 			return &out, nil
// 		}
// 	}
// 	return nil, nil
// }

// func (r *inMemoryAnalysisRepo) WithTransaction(_ context.Context, fn func(interfaces.AnalysisRepository) error) error {
// 	if fn == nil {
// 		return nil
// 	}
// 	return fn(r)
// }

// func (r *inMemoryAnalysisRepo) CreateAnalysis(_ context.Context, item *types.Analysis) error {
// 	if item == nil {
// 		return nil
// 	}
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	cloned := *item
// 	r.analyses[strings.TrimSpace(item.AnalysisID)] = &cloned
// 	return nil
// }

// func (r *inMemoryAnalysisRepo) TryMarkAnalysisRunning(_ context.Context, _ string, _ time.Time, _ time.Time) (bool, error) {
// 	return true, nil
// }

// func (r *inMemoryAnalysisRepo) UpdateAnalysisByAnalysisID(_ context.Context, analysisID string, values map[string]any) error {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	item, ok := r.analyses[strings.TrimSpace(analysisID)]
// 	if !ok {
// 		return nil
// 	}
// 	if v, ok := values["output_dir"]; ok {
// 		item.OutputDir = strings.TrimSpace(dynamicToString(v))
// 	}
// 	if v, ok := values["cache_type"]; ok {
// 		item.CacheType = dynamicIntFromAny(v, item.CacheType)
// 	}
// 	return nil
// }

// func (r *inMemoryAnalysisRepo) ListAnalysisNodesByAnalysisID(_ context.Context, analysisID string) ([]*types.AnalysisNode, error) {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	items := r.nodesByAID[strings.TrimSpace(analysisID)]
// 	out := make([]*types.AnalysisNode, 0, len(items))
// 	for _, item := range items {
// 		if item == nil {
// 			continue
// 		}
// 		cloned := *item
// 		out = append(out, &cloned)
// 	}
// 	return out, nil
// }

// func (r *inMemoryAnalysisRepo) PageAnalysisNodesByProjectID(_ context.Context, pagination *types.Pagination, projectID int64, scriptID string) ([]*types.AnalysisNode, int64, error) {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()

// 	// projectID = strings.TrimSpace(projectID)
// 	// scriptID = strings.TrimSpace(scriptID)
// 	filtered := make([]*types.AnalysisNode, 0)
// 	for _, node := range r.nodesByID {
// 		if node == nil {
// 			continue
// 		}
// 		if node.ProjectID != projectID {
// 			continue
// 		}
// 		if scriptID != "" && node.ScriptID != scriptID {
// 			continue
// 		}
// 		cloned := *node
// 		filtered = append(filtered, &cloned)
// 	}

// 	total := int64(len(filtered))
// 	if pagination == nil {
// 		return filtered, total, nil
// 	}

// 	start := pagination.Offset()
// 	if start >= len(filtered) {
// 		return []*types.AnalysisNode{}, total, nil
// 	}
// 	end := start + pagination.Limit()
// 	if end > len(filtered) {
// 		end = len(filtered)
// 	}
// 	return filtered[start:end], total, nil
// }

// func (r *inMemoryAnalysisRepo) ListAnalysisEdgesByAnalysisID(_ context.Context, _ string) ([]*types.AnalysisEdge, error) {
// 	return nil, nil
// }

// func (r *inMemoryAnalysisRepo) UpdateAnalysisNodeByAnalysisNodeID(_ context.Context, analysisNodeID string, values map[string]any) error {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	needle := strings.TrimSpace(analysisNodeID)
// 	for _, node := range r.nodesByID {
// 		if node == nil || strings.TrimSpace(node.AnalysisNodeID) != needle {
// 			continue
// 		}
// 		if v, ok := values["status"]; ok {
// 			node.Status = strings.TrimSpace(dynamicToString(v))
// 		}
// 		if v, ok := values["resolved_outputs"]; ok {
// 			node.ResolvedOutputs = types.JSONMap(dynamicToMap(v))
// 		}
// 		if v, ok := values["resolved_inputs"]; ok {
// 			node.ResolvedInputs = types.JSONMap(dynamicToMap(v))
// 		}
// 		return nil
// 	}
// 	return nil
// }

// func (r *inMemoryAnalysisRepo) ClaimNextReadyNode(_ context.Context, _ string, _ string, _ string) (*types.AnalysisNode, error) {
// 	return nil, nil
// }

// func (r *inMemoryAnalysisRepo) DeleteAnalysisNodesByAnalysisID(_ context.Context, analysisID string) error {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	analysisID = strings.TrimSpace(analysisID)
// 	for id, node := range r.nodesByID {
// 		if node != nil && strings.TrimSpace(node.AnalysisID) == analysisID {
// 			delete(r.nodesByID, id)
// 		}
// 	}
// 	delete(r.nodesByAID, analysisID)
// 	return nil
// }

// func (r *inMemoryAnalysisRepo) CreateAnalysisNodes(_ context.Context, items []*types.AnalysisNode) error {
// 	r.mu.Lock()
// 	defer r.mu.Unlock()
// 	for _, item := range items {
// 		if item == nil {
// 			continue
// 		}
// 		cloned := *item
// 		if cloned.ID <= 0 {
// 			r.nextNodeIDSeq++
// 			cloned.ID = r.nextNodeIDSeq
// 		}
// 		r.nodesByID[cloned.ID] = &cloned
// 		aid := strings.TrimSpace(cloned.AnalysisID)
// 		r.nodesByAID[aid] = append(r.nodesByAID[aid], &cloned)
// 	}
// 	return nil
// }

// func (r *inMemoryAnalysisRepo) DeleteAnalysisEdgesByAnalysisID(_ context.Context, _ string) error {
// 	return nil
// }

// func (r *inMemoryAnalysisRepo) CreateAnalysisEdges(_ context.Context, _ []*types.AnalysisEdge) error {
// 	return nil
// }

// type runtimeEventBridge struct {
// 	analysisID string
// 	kernel     *dataflowKernel
// 	runtime    *persistentDataflowRuntime
// 	mu         sync.Mutex
// 	err        error
// }

// func (h *runtimeEventBridge) Handle(evt event.Event) {
// 	runtimeEvt, ok := evt.(dagruntime.RuntimeEvent)
// 	if !ok || strings.TrimSpace(runtimeEvt.AnalysisID) != strings.TrimSpace(h.analysisID) {
// 		return
// 	}
// 	if h.runtime != nil {
// 		h.runtime.onRuntimeEvent(runtimeEvt)
// 	}
// 	if strings.TrimSpace(runtimeEvt.Name) != dagruntime.EventNodeCompleted {
// 		return
// 	}
// 	if h.kernel == nil {
// 		return
// 	}
// 	if err := h.kernel.onNodeCompleted(context.Background(), runtimeEvt.AnalysisNodeID); err != nil {
// 		h.mu.Lock()
// 		if h.err == nil {
// 			h.err = err
// 		}
// 		h.mu.Unlock()
// 	}
// }

// func (h *runtimeEventBridge) Err() error {
// 	h.mu.Lock()
// 	defer h.mu.Unlock()
// 	return h.err
// }

// type simulatedDispatcher struct {
// 	repo        *inMemoryAnalysisRepo
// 	bus         event.Bus
// 	analysisID  string
// 	delayByNode map[string]time.Duration
// 	asyncByNode map[string]bool
// 	mu          sync.Mutex
// 	dispatched  []string
// 	dispatchIDs []int64
// 	err         error
// }

// func (d *simulatedDispatcher) Dispatch(ctx context.Context, analysisNodeID int64) error {
// 	node, err := d.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
// 	if err != nil {
// 		return err
// 	}
// 	if node == nil {
// 		return fmt.Errorf("node not found: %d", analysisNodeID)
// 	}

// 	d.mu.Lock()
// 	d.dispatched = append(d.dispatched, strings.TrimSpace(node.NodeID))
// 	d.dispatchIDs = append(d.dispatchIDs, node.ID)
// 	d.mu.Unlock()

// 	nodeID := strings.TrimSpace(node.NodeID)
// 	if delay := d.delayByNode[nodeID]; delay > 0 {
// 		if async := d.asyncByNode[nodeID]; async {
// 			go d.completeNodeWithDelay(*node, delay)
// 			return nil
// 		}
// 		time.Sleep(delay)
// 	}

// 	if async := d.asyncByNode[nodeID]; async {
// 		go d.completeNodeWithDelay(*node, 0)
// 		return nil
// 	}

// 	return d.completeNode(ctx, node)
// }

// func (d *simulatedDispatcher) completeNodeWithDelay(node types.AnalysisNode, delay time.Duration) {
// 	if delay > 0 {
// 		time.Sleep(delay)
// 	}
// 	if err := d.completeNode(context.Background(), &node); err != nil {
// 		d.mu.Lock()
// 		if d.err == nil {
// 			d.err = err
// 		}
// 		d.mu.Unlock()
// 	}
// }

// func (d *simulatedDispatcher) completeNode(ctx context.Context, node *types.AnalysisNode) error {
// 	if node == nil {
// 		return nil
// 	}

// 	outputs := simulatedNodeOutputs(node)
// 	if err := d.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
// 		"status":           dagruntime.StatusDone,
// 		"resolved_outputs": outputs,
// 	}); err != nil {
// 		return err
// 	}

// 	if d.bus != nil {
// 		d.bus.Publish(dagruntime.RuntimeEvent{
// 			Name:           dagruntime.EventNodeCompleted,
// 			AnalysisID:     strings.TrimSpace(d.analysisID),
// 			AnalysisNodeID: node.ID,
// 			NodeID:         strings.TrimSpace(node.NodeID),
// 			OccurredAt:     time.Now().UTC(),
// 			Payload: map[string]any{
// 				"status": dagruntime.StatusDone,
// 			},
// 		})
// 	}
// 	return nil
// }

// func (d *simulatedDispatcher) DispatchedNodeIDs() []string {
// 	d.mu.Lock()
// 	defer d.mu.Unlock()
// 	out := make([]string, len(d.dispatched))
// 	copy(out, d.dispatched)
// 	return out
// }

// func (d *simulatedDispatcher) Err() error {
// 	d.mu.Lock()
// 	defer d.mu.Unlock()
// 	return d.err
// }

// func simulatedNodeOutputs(node *types.AnalysisNode) map[string]any {
// 	nodeID := strings.TrimSpace(node.NodeID)
// 	resolvedInputs := map[string]any(node.ResolvedInputs)
// 	switch nodeID {
// 	case "759d6899-e632-400f-9b11-6a8b7b6e2042":
// 		reads := dynamicToMap(resolvedInputs["reads"])
// 		r1 := strings.TrimSpace(dynamicToString(reads["FASTQ_R1"]))
// 		r2 := strings.TrimSpace(dynamicToString(reads["FASTQ_R2"]))
// 		if r1 == "" {
// 			r1 = "unknown_R1"
// 		}
// 		if r2 == "" {
// 			r2 = "unknown_R2"
// 		}
// 		return map[string]any{
// 			"fastq1_trimmed": r1 + ".trimmed",
// 			"fastq2_trimmed": r2 + ".trimmed",
// 		}
// 	case "7afe22e2-1e25-4261-b7c7-3f7d7ee52222":
// 		index := strings.TrimSpace(dynamicToString(resolvedInputs["reference_genome"]))
// 		if index == "" {
// 			index = "/data/index"
// 		}
// 		return map[string]any{"index": index}
// 	case "cbd1a1cc-ca62-46da-8713-b0e868a2d44f":
// 		return map[string]any{"bam": fmt.Sprintf("%s.bam", strings.TrimSpace(node.AnalysisNodeID))}
// 	case "e3d00582-2a99-4b02-a687-65e556f7aefa":
// 		return map[string]any{"vcf": "joint_calling.vcf"}
// 	default:
// 		return map[string]any{}
// 	}
// }

// func TestDataflowOrchestratorV3_ParamsJSON_FullPipelineWithSimulatedDispatcher(t *testing.T) {
// 	ctx := context.Background()
// 	ensureTestSnowflake(t)
// 	analysisID, parseResult, dagDefinition := loadDataflowParamsFixture(t)

// 	repo := newInMemoryAnalysisRepo()
// 	if err := repo.CreateAnalysis(ctx, &types.Analysis{
// 		AnalysisID: analysisID,
// 		OutputDir:  "/tmp/dataflow-orchestrator-v3-test",
// 		CacheType:  types.CacheTypeReuseExistingNode,
// 	}); err != nil {
// 		t.Fatalf("seed analysis failed: %v", err)
// 	}

// 	o := &dataflowDagOrchestratorV3{}
// 	spec := o.buildGraphSpec(analysisID, dagDefinition)
// 	bus := newSyncEventBus()

// 	var kernel *dataflowKernel
// 	runtime := &persistentDataflowRuntime{
// 		repo: repo,
// 		onNodeSubmitChange: func(nodeID string, delta int) {
// 			if kernel == nil {
// 				return
// 			}
// 			kernel.adjustSubmittedCount(nodeID, delta)
// 		},
// 		buildPersistParams: func(req DataflowProcessRunRequest) (*DataflowAnalysisNodePersistParams, bool) {
// 			if kernel == nil {
// 				return nil, false
// 			}
// 			return kernel.buildAnalysisNodePersistParams(req)
// 		},
// 	}

// 	dispatcher := &simulatedDispatcher{repo: repo, bus: bus, analysisID: analysisID}
// 	runtime.dispatchFn = dispatcher.Dispatch

// 	kernel = newDataflowKernel(spec, runtime, parseResult)
// 	kernel.repo = repo

// 	bridge := &runtimeEventBridge{analysisID: analysisID, kernel: kernel, runtime: runtime}
// 	bus.Subscribe(bridge)

// 	if err := kernel.bootstrapSourceProcesses(ctx); err != nil {
// 		t.Fatalf("bootstrap source processes failed: %v", err)
// 	}
// 	if err := bridge.Err(); err != nil {
// 		t.Fatalf("runtime event bridge failed: %v", err)
// 	}

// 	nodes, err := repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
// 	if err != nil {
// 		t.Fatalf("list analysis nodes failed: %v", err)
// 	}
// 	if len(nodes) != 6 {
// 		t.Fatalf("unexpected persisted node count: got=%d want=%d", len(nodes), 6)
// 	}

// 	nodeCounts := map[string]int{}
// 	for _, n := range nodes {
// 		nodeCounts[strings.TrimSpace(n.NodeID)]++
// 		if strings.TrimSpace(strings.ToLower(n.Status)) != dagruntime.StatusDone {
// 			t.Fatalf("node should be done: node_id=%s status=%s", n.NodeID, n.Status)
// 		}
// 	}

// 	if nodeCounts["759d6899-e632-400f-9b11-6a8b7b6e2042"] != 2 {
// 		t.Fatalf("fastp instances mismatch: got=%d want=%d", nodeCounts["759d6899-e632-400f-9b11-6a8b7b6e2042"], 2)
// 	}
// 	if nodeCounts["7afe22e2-1e25-4261-b7c7-3f7d7ee52222"] != 1 {
// 		t.Fatalf("bwa index instances mismatch: got=%d want=%d", nodeCounts["7afe22e2-1e25-4261-b7c7-3f7d7ee52222"], 1)
// 	}
// 	if nodeCounts["cbd1a1cc-ca62-46da-8713-b0e868a2d44f"] != 2 {
// 		t.Fatalf("bwa instances mismatch: got=%d want=%d", nodeCounts["cbd1a1cc-ca62-46da-8713-b0e868a2d44f"], 2)
// 	}
// 	if nodeCounts["e3d00582-2a99-4b02-a687-65e556f7aefa"] != 1 {
// 		t.Fatalf("joint_calling instances mismatch: got=%d want=%d", nodeCounts["e3d00582-2a99-4b02-a687-65e556f7aefa"], 1)
// 	}

// 	var jointCallingNode *types.AnalysisNode
// 	for _, n := range nodes {
// 		if strings.TrimSpace(n.NodeID) == "e3d00582-2a99-4b02-a687-65e556f7aefa" {
// 			jointCallingNode = n
// 			break
// 		}
// 	}
// 	if jointCallingNode == nil {
// 		t.Fatalf("joint_calling node not found")
// 	}
// 	bams, ok := any(jointCallingNode.ResolvedInputs["bam"]).([]any)
// 	if !ok {
// 		t.Fatalf("joint_calling bam input should be []any, got=%T", jointCallingNode.ResolvedInputs["bam"])
// 	}
// 	if len(bams) != 2 {
// 		t.Fatalf("joint_calling bam list size mismatch: got=%d want=%d", len(bams), 2)
// 	}

// 	if inflight := runtime.InflightDispatches(); inflight != 0 {
// 		t.Fatalf("inflight dispatches should be 0 after completion, got=%d", inflight)
// 	}

// 	dispatched := dispatcher.DispatchedNodeIDs()
// 	if len(dispatched) != 6 {
// 		t.Fatalf("dispatcher dispatch count mismatch: got=%d want=%d", len(dispatched), 6)
// 	}
// }

// func TestDataflowOrchestratorV3_ParamsJSON_SlowFastpDoesNotEmitToClosedChannel(t *testing.T) {
// 	ctx := context.Background()
// 	ensureTestSnowflake(t)
// 	analysisID, parseResult, dagDefinition := loadDataflowParamsFixture(t)

// 	repo := newInMemoryAnalysisRepo()
// 	if err := repo.CreateAnalysis(ctx, &types.Analysis{
// 		AnalysisID: analysisID,
// 		OutputDir:  "/tmp/dataflow-orchestrator-v3-test",
// 		CacheType:  types.CacheTypeReuseExistingNode,
// 	}); err != nil {
// 		t.Fatalf("seed analysis failed: %v", err)
// 	}

// 	o := &dataflowDagOrchestratorV3{}
// 	spec := o.buildGraphSpec(analysisID, dagDefinition)
// 	bus := newSyncEventBus()

// 	var kernel *dataflowKernel
// 	runtime := &persistentDataflowRuntime{
// 		repo: repo,
// 		onNodeSubmitChange: func(nodeID string, delta int) {
// 			if kernel == nil {
// 				return
// 			}
// 			kernel.adjustSubmittedCount(nodeID, delta)
// 		},
// 		buildPersistParams: func(req DataflowProcessRunRequest) (*DataflowAnalysisNodePersistParams, bool) {
// 			if kernel == nil {
// 				return nil, false
// 			}
// 			return kernel.buildAnalysisNodePersistParams(req)
// 		},
// 	}

// 	dispatcher := &simulatedDispatcher{
// 		repo:       repo,
// 		bus:        bus,
// 		analysisID: analysisID,
// 		delayByNode: map[string]time.Duration{
// 			"759d6899-e632-400f-9b11-6a8b7b6e2042": 120 * time.Millisecond,
// 		},
// 		asyncByNode: map[string]bool{
// 			"759d6899-e632-400f-9b11-6a8b7b6e2042": true,
// 		},
// 	}
// 	runtime.dispatchFn = dispatcher.Dispatch

// 	kernel = newDataflowKernel(spec, runtime, parseResult)
// 	kernel.repo = repo

// 	bridge := &runtimeEventBridge{analysisID: analysisID, kernel: kernel, runtime: runtime}
// 	bus.Subscribe(bridge)

// 	if err := kernel.bootstrapSourceProcesses(ctx); err != nil {
// 		t.Fatalf("bootstrap source processes failed: %v", err)
// 	}

// 	deadline := time.Now().Add(3 * time.Second)
// 	for {
// 		if err := bridge.Err(); err != nil {
// 			t.Fatalf("runtime event bridge failed: %v", err)
// 		}
// 		if err := dispatcher.Err(); err != nil {
// 			t.Fatalf("simulated dispatcher failed: %v", err)
// 		}
// 		nodes, err := repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
// 		if err != nil {
// 			t.Fatalf("list analysis nodes failed: %v", err)
// 		}
// 		allDone := len(nodes) >= 5
// 		if allDone {
// 			for _, n := range nodes {
// 				if strings.TrimSpace(strings.ToLower(n.Status)) != dagruntime.StatusDone {
// 					allDone = false
// 					break
// 				}
// 			}
// 		}
// 		if allDone && runtime.InflightDispatches() == 0 {
// 			break
// 		}
// 		if time.Now().After(deadline) {
// 			t.Fatalf("timeout waiting pipeline completion: nodes=%d inflight=%d", len(nodes), runtime.InflightDispatches())
// 		}
// 		time.Sleep(20 * time.Millisecond)
// 	}

// 	nodes, err := repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
// 	if err != nil {
// 		t.Fatalf("list analysis nodes failed: %v", err)
// 	}

// 	nodeCounts := map[string]int{}
// 	for _, n := range nodes {
// 		nodeCounts[strings.TrimSpace(n.NodeID)]++
// 	}
// 	if nodeCounts["759d6899-e632-400f-9b11-6a8b7b6e2042"] != 2 {
// 		t.Fatalf("fastp instances mismatch: got=%d want=%d", nodeCounts["759d6899-e632-400f-9b11-6a8b7b6e2042"], 2)
// 	}
// 	if nodeCounts["7afe22e2-1e25-4261-b7c7-3f7d7ee52222"] != 1 {
// 		t.Fatalf("bwa index instances mismatch: got=%d want=%d", nodeCounts["7afe22e2-1e25-4261-b7c7-3f7d7ee52222"], 1)
// 	}
// 	if nodeCounts["cbd1a1cc-ca62-46da-8713-b0e868a2d44f"] != 2 {
// 		t.Fatalf("bwa instances mismatch: got=%d want=%d", nodeCounts["cbd1a1cc-ca62-46da-8713-b0e868a2d44f"], 2)
// 	}
// 	if nodeCounts["e3d00582-2a99-4b02-a687-65e556f7aefa"] < 1 {
// 		t.Fatalf("joint_calling instances mismatch: got=%d want>=1", nodeCounts["e3d00582-2a99-4b02-a687-65e556f7aefa"])
// 	}
// }

// func loadDataflowParamsFixture(t *testing.T) (string, map[string]any, map[string]any) {
// 	t.Helper()
// 	root := map[string]any{}
// 	if err := json.Unmarshal([]byte(dataflowParamsFixtureJSON), &root); err != nil {
// 		t.Fatalf("unmarshal params fixture failed: %v", err)
// 	}
// 	analysisID := strings.TrimSpace(dynamicToString(root["analysis_id"]))
// 	if analysisID == "" {
// 		t.Fatalf("analysis_id is required in fixture")
// 	}
// 	parseResult := dynamicToMap(root["parse_analysis_result"])
// 	dagDefinition := dynamicToMap(root["dag_definition"])
// 	return analysisID, parseResult, dagDefinition
// }

// const dataflowParamsFixtureJSON = `
// {
//   "analysis_id": "9065a0f9-fe2a-4a8c-bb13-c947b54ada30",
//   "dag_definition": {
//     "edges": [
//       {
//         "id": "xy-edge__759d6899-e632-400f-9b11-6a8b7b6e2042_1fastq1_trimmed-cbd1a1cc-ca62-46da-8713-b0e868a2d44f_1fastq1",
//         "source": "759d6899-e632-400f-9b11-6a8b7b6e2042",
//         "sourceHandle": "fastq1_trimmed",
//         "target": "cbd1a1cc-ca62-46da-8713-b0e868a2d44f",
//         "targetHandle": "fastq1"
//       },
//       {
//         "id": "xy-edge__759d6899-e632-400f-9b11-6a8b7b6e2042_1fastq2_trimmed-cbd1a1cc-ca62-46da-8713-b0e868a2d44f_1fastq2",
//         "source": "759d6899-e632-400f-9b11-6a8b7b6e2042",
//         "sourceHandle": "fastq2_trimmed",
//         "target": "cbd1a1cc-ca62-46da-8713-b0e868a2d44f",
//         "targetHandle": "fastq2"
//       },
//       {
//         "id": "xy-edge__cbd1a1cc-ca62-46da-8713-b0e868a2d44f_1bam-e3d00582-2a99-4b02-a687-65e556f7aefa_1bam",
//         "source": "cbd1a1cc-ca62-46da-8713-b0e868a2d44f",
//         "sourceHandle": "bam",
//         "target": "e3d00582-2a99-4b02-a687-65e556f7aefa",
//         "targetHandle": "bam"
//       },
//       {
//         "id": "xy-edge__7afe22e2-1e25-4261-b7c7-3f7d7ee52222_1index-cbd1a1cc-ca62-46da-8713-b0e868a2d44f_1index",
//         "source": "7afe22e2-1e25-4261-b7c7-3f7d7ee52222",
//         "sourceHandle": "index",
//         "target": "cbd1a1cc-ca62-46da-8713-b0e868a2d44f",
//         "targetHandle": "index"
//       }
//     ],
//     "nodes": [
//       {
//         "color": "lime",
//         "gather": {
//           "field": "bam",
//           "mode": "list"
//         },
//         "icon": "scissors",
//         "id": "e3d00582-2a99-4b02-a687-65e556f7aefa",
//         "inputs": {
//           "bam": {
//             "type": "BaseInput"
//           }
//         },
//         "name": "joint_calling",
//         "node_id": "e3d00582-2a99-4b02-a687-65e556f7aefa_1",
//         "outputs": {
//           "vcf": {
//             "type": "file"
//           }
//         },
//         "script_id": "e3d00582-2a99-4b02-a687-65e556f7aefa"
//       },
//       {
//         "color": "green",
//         "icon": "scissors",
//         "id": "cbd1a1cc-ca62-46da-8713-b0e868a2d44f",
//         "inputs": {
//           "fastq1": {
//             "label": "fastq1",
//             "required": true,
//             "type": "BaseInput"
//           },
//           "fastq2": {
//             "label": "fastq2",
//             "required": true,
//             "type": "BaseInput"
//           },
//           "index": {
//             "label": "index",
//             "required": true,
//             "type": "BaseInput"
//           }
//         },
//         "name": "bwa",
//         "node_id": "cbd1a1cc-ca62-46da-8713-b0e868a2d44f_1",
//         "outputs": {
//           "bam": {
//             "type": "file"
//           }
//         },
//         "script_id": "cbd1a1cc-ca62-46da-8713-b0e868a2d44f"
//       },
//       {
//         "color": "magenta",
//         "icon": "scissors",
//         "id": "759d6899-e632-400f-9b11-6a8b7b6e2042",
//         "inputs": {
//           "fastq1": {
//             "required": true,
//             "type": "BaseInput"
//           },
//           "fastq2": {
//             "required": true,
//             "type": "BaseInput"
//           }
//         },
//         "name": "fastp",
//         "node_id": "759d6899-e632-400f-9b11-6a8b7b6e2042_1",
//         "outputs": {
//           "fastq1_trimmed": {
//             "type": "file"
//           },
//           "fastq2_trimmed": {
//             "type": "file"
//           }
//         },
//         "scatter": {
//           "field": "reads",
//           "mode": "each"
//         },
//         "script_id": "759d6899-e632-400f-9b11-6a8b7b6e2042"
//       },
//       {
//         "color": "green",
//         "icon": "scissors",
//         "id": "7afe22e2-1e25-4261-b7c7-3f7d7ee52222",
//         "inputs": {
//           "reference_genome": {
//             "label": "Reference Genome Index Path",
//             "required": true,
//             "rules": [
//               {
//                 "message": "Please specify the reference genome index path",
//                 "required": true
//               }
//             ],
//             "type": "BaseInput"
//           }
//         },
//         "name": "bwa index",
//         "node_id": "7afe22e2-1e25-4261-b7c7-3f7d7ee52222_1",
//         "outputs": {
//           "index": {
//             "type": "file"
//           }
//         },
//         "script_id": "7afe22e2-1e25-4261-b7c7-3f7d7ee52222"
//       }
//     ]
//   },
//   "parse_analysis_result": {
//     "analysis_name": "ssss",
//     "group_field": "group",
//     "groups": [
//       "reads"
//     ],
//     "output_name": "bwa_alignment",
//     "reads": [
//       {
//         "FASTQ_R1": "/data/test2_1.fq",
//         "FASTQ_R2": "/data/test2_2.fq",
//         "ID": "2065805577694482432"
//       },
//       {
//         "FASTQ_R1": "/data/test1_1.fq",
//         "FASTQ_R2": "/data/test1_2.fq",
//         "ID": "2065805403978993664"
//       }
//     ],
//     "reference_genome": "/data/index"
//   }
// }
// `
