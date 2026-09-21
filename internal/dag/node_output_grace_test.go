package dag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
)

// scriptedOutputResolver reports a missing outputs.json for its first `misses`
// calls and a complete result afterwards. That is the shape the completion path
// sees when a container's outputs land between the exit event and a retry: the
// first read misses, the file is there a moment later.
type scriptedOutputResolver struct {
	misses int
	calls  int
}

func (r *scriptedOutputResolver) Resolve(_ *types.AnalysisNode, _ map[string]any) (map[string]any, bool, []string) {
	r.calls++
	if r.calls <= r.misses {
		return map[string]any{}, true, nil
	}
	return map[string]any{"feature_test_path": map[string]any{"path": "/tmp/feature_test_results.tsv"}}, false, nil
}

func outputGraceTestNode(t *testing.T) *types.AnalysisNode {
	t.Helper()
	return &types.AnalysisNode{
		NodeID:         "output-grace-node",
		OutputDir:      t.TempDir(),
		OutputPatterns: types.JSONMap{"feature_test_path": map[string]any{"type": "file"}},
	}
}

// A node whose outputs become readable on a later attempt must be resolved as
// done - this is the regression the retry loop exists for, and it is the case
// that used to be reported as "output validation failed: missing output".
func TestResolveOutputsWithGraceResolvesLateOutput(t *testing.T) {
	node := outputGraceTestNode(t)
	resolver := &scriptedOutputResolver{misses: 1}
	c := &NodeCompletionCoordinator{graceWait: 5 * time.Second, graceInterval: 5 * time.Millisecond}

	resolved, errs := c.resolveOutputsWithGrace(context.Background(), resolver, node)

	if len(errs) != 0 {
		t.Fatalf("expected no hard errors, got %v", errs)
	}
	if _, ok := resolved["feature_test_path"]; !ok {
		t.Fatalf("expected the late output to be resolved, got %v", resolved)
	}
	if got := validateOutputPatterns(node, resolved); len(got) != 0 {
		t.Fatalf("expected no validation errors once the output is readable, got %v", got)
	}
	if resolver.calls != 2 {
		t.Fatalf("expected the output to be resolved on the first retry, got %d resolves", resolver.calls)
	}
}

// A node that never produces its declared output still has to surface it, so the
// grace window may not swallow the failure - it only delays the verdict.
func TestResolveOutputsWithGraceGivesUpAfterWindow(t *testing.T) {
	node := outputGraceTestNode(t)
	resolver := &scriptedOutputResolver{misses: 1 << 30}
	c := &NodeCompletionCoordinator{graceWait: 60 * time.Millisecond, graceInterval: 10 * time.Millisecond}

	resolved, errs := c.resolveOutputsWithGrace(context.Background(), resolver, node)

	if len(errs) != 0 {
		t.Fatalf("a missing file is not a hard error, got %v", errs)
	}
	if resolver.calls < 2 {
		t.Fatalf("expected the retry loop to keep trying, got %d resolves", resolver.calls)
	}
	got := validateOutputPatterns(node, resolved)
	if len(got) != 1 || !strings.Contains(got[0], "feature_test_path") {
		t.Fatalf("expected the missing handle to be reported, got %v", got)
	}
}

// The retry path refreshes the directory view between attempts; a missing output
// directory must not turn that refresh into a crash.
func TestResolveOutputsWithGraceToleratesMissingOutputDir(t *testing.T) {
	node := outputGraceTestNode(t)
	node.OutputDir = filepath.Join(t.TempDir(), "gone")
	resolver := &scriptedOutputResolver{misses: 1}
	c := &NodeCompletionCoordinator{graceWait: time.Second, graceInterval: 5 * time.Millisecond}

	if _, errs := c.resolveOutputsWithGrace(context.Background(), resolver, node); len(errs) != 0 {
		t.Fatalf("expected no hard errors, got %v", errs)
	}
}

// The probe has to stat before it lists the directory, otherwise the listing is
// what makes the file visible and the disagreement it is looking for can never be
// observed.
func TestOutputProbeDetailStatsBeforeListingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "outputs.json"), []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("write outputs.json: %v", err)
	}

	detail, hidden := outputProbeDetail(dir, "outputs.json", "ghost.tsv")

	if len(hidden) != 0 {
		t.Fatalf("no name here is hidden by a stale lookup, got %v", hidden)
	}
	before := strings.Index(detail, "stat_before_readdir=[")
	listing := strings.Index(detail, "readdir=[")
	after := strings.Index(detail, "stat_after_readdir=[")
	if before < 0 || listing < 0 || after < 0 {
		t.Fatalf("expected all three sections in the probe line, got %q", detail)
	}
	if !(before < listing && listing < after) {
		t.Fatalf("expected stat-before, listing, then stat-after, got %q", detail)
	}
	if !strings.Contains(detail, "outputs.json=ok(") {
		t.Fatalf("expected the existing output to be reported readable, got %q", detail)
	}
	if !strings.Contains(detail, "ghost.tsv=") {
		t.Fatalf("expected the absent file to be reported, got %q", detail)
	}
}
