package dag

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// recordingDataRepo is a minimal DataRepository that only implements the two
// methods recordNodeOutputFiles reaches. Everything else is left to the embedded
// nil interface on purpose: a call outside this scope is a test bug, not a case
// worth faking.
type recordingDataRepo struct {
	interfaces.DataRepository
	files  map[string]*types.File
	nextID int64
}

func newRecordingDataRepo() *recordingDataRepo {
	return &recordingDataRepo{files: map[string]*types.File{}, nextID: 100}
}

func (r *recordingDataRepo) GetFileByAnalysisNodeIDAndFileKey(_ context.Context, analysisNodeID int64, fileKey string) (*types.File, error) {
	file, ok := r.files[fileKey]
	if !ok || file.AnalysisNodeID != analysisNodeID {
		return nil, gorm.ErrRecordNotFound
	}
	return file, nil
}

func (r *recordingDataRepo) CreateFile(_ context.Context, file *types.File) error {
	r.nextID++
	file.ID = r.nextID
	r.files[file.FileKey] = file
	return nil
}

// TestRecordNodeOutputFilesAgentNodeWalksResolvedOutputs covers the agent
// (command) node path: output_patterns is empty, so every resolved output whose
// value points at a real file is recorded, relative paths are resolved against
// the node output dir, and the container metadata that buildResolvedOutputs adds
// is not recorded as a file.
func TestRecordNodeOutputFilesAgentNodeWalksResolvedOutputs(t *testing.T) {
	node := &types.AnalysisNode{
		ID:             7,
		ProjectID:      1,
		AnalysisID:     34,
		NodeID:         "node-agent",
		NodeName:       "agent analysis",
		CreationSource: types.AnalysisNodeCreationSourceAgent,
		OutputPatterns: types.JSONMap{},
	}
	node.SetBaseDir(t.TempDir())
	dir := node.OutputDirectory()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create output dir: %v", err)
	}
	report := filepath.Join(dir, "report.md")
	if err := os.WriteFile(report, []byte("report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	sub := filepath.Join(dir, "tables")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("create tables dir: %v", err)
	}
	counts := filepath.Join(sub, "counts.tsv")
	if err := os.WriteFile(counts, []byte("a\tb\n"), 0o644); err != nil {
		t.Fatalf("write counts: %v", err)
	}

	repo := newRecordingDataRepo()
	c := &NodeCompletionCoordinator{dataRepo: repo}

	outputs := map[string]any{
		"report":                map[string]any{"path": report, "key": "report_md"},
		"counts":                "tables/counts.tsv",
		"missing":               "tables/absent.tsv",
		"tables":                map[string]any{"path": sub},
		"container_instance_id": int64(3),
		"container_status":      "exited",
		"container_ip":          "172.18.0.2",
	}

	fileIDs := c.recordNodeOutputFiles(context.Background(), node, outputs)

	if _, ok := fileIDs["report"]; !ok {
		t.Fatalf("report output not recorded, got %v", fileIDs)
	}
	if _, ok := fileIDs["counts"]; !ok {
		t.Fatalf("counts output not recorded, got %v", fileIDs)
	}
	if len(fileIDs) != 2 {
		t.Fatalf("expected only the two existing files recorded, got %v", fileIDs)
	}

	if got := repo.files["report_md"]; got == nil || got.Path != report {
		t.Fatalf("report file row = %+v, want path %s", got, report)
	}
	countsRow := repo.files["counts"]
	if countsRow == nil {
		t.Fatalf("counts file row missing, recorded keys = %v", keysOf(repo.files))
	}
	if countsRow.Path != counts {
		t.Fatalf("counts path = %s, want %s", countsRow.Path, counts)
	}
	if countsRow.Role != DefaultNodeOutputRole {
		t.Fatalf("counts role = %q, want %q", countsRow.Role, DefaultNodeOutputRole)
	}
}

// TestRecordNodeOutputFilesScriptNodeRecordsEveryFile pins the unified rule: a
// script node records every resolved output whose value names an existing regular
// file, not only the handles declared as file outputs. The declared handle keeps
// the role declared by its pattern; an output that no output_patterns declared is
// published with DefaultNodeOutputRole. Declared non-file values ("stats" = "42")
// are not files and are therefore not recorded.
func TestRecordNodeOutputFilesScriptNodeRecordsEveryFile(t *testing.T) {
	node := &types.AnalysisNode{
		ID:             9,
		ProjectID:      1,
		AnalysisID:     34,
		NodeID:         "node-script",
		NodeName:       "align",
		ScriptID:       11,
		CreationSource: types.AnalysisNodeCreationSourceScript,
		OutputPatterns: types.JSONMap{
			"bam":   map[string]any{"type": "file", "role": "table"},
			"stats": map[string]any{"type": "string"},
		},
	}
	node.SetBaseDir(t.TempDir())
	dir := node.OutputDirectory()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create output dir: %v", err)
	}
	bam := filepath.Join(dir, "sample.bam")
	if err := os.WriteFile(bam, []byte("bam"), 0o644); err != nil {
		t.Fatalf("write bam: %v", err)
	}
	extra := filepath.Join(dir, "extra.txt")
	if err := os.WriteFile(extra, []byte("extra"), 0o644); err != nil {
		t.Fatalf("write extra: %v", err)
	}

	repo := newRecordingDataRepo()
	c := &NodeCompletionCoordinator{dataRepo: repo}

	fileIDs := c.recordNodeOutputFiles(context.Background(), node, map[string]any{
		"bam":   map[string]any{"path": bam, "key": "sample_bam"},
		"stats": "42",
		"extra": extra,
	})

	if len(fileIDs) != 2 {
		t.Fatalf("expected the declared file and the undeclared file recorded, got %v", fileIDs)
	}
	if got := repo.files["sample_bam"]; got == nil || got.Role != types.RoleTable {
		t.Fatalf("bam file row = %+v, want role %q", got, types.RoleTable)
	}
	extraRow := repo.files["extra"]
	if extraRow == nil || extraRow.Path != extra {
		t.Fatalf("extra file row = %+v, want path %s", extraRow, extra)
	}
	if extraRow.Role != DefaultNodeOutputRole {
		t.Fatalf("extra role = %q, want %q", extraRow.Role, DefaultNodeOutputRole)
	}
	if repo.files["stats"] != nil {
		t.Fatalf("non-file value was recorded as a file: %+v", repo.files["stats"])
	}
}

func keysOf(m map[string]*types.File) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
