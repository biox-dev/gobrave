package dynamic

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/types"
)

// NodeArtifactFingerprinter predicts a node's runtime artifacts and records their
// digests on the node.
//
// It is deliberately narrower than prepare.NodeRuntimePreparer: the scheduler only
// needs the digests to decide whether a persisted node can be reused, it must not
// own execution-time preparation (output cleanup, script symlinks, ...) which stays
// with the dispatcher. Depending on the full preparer here is what made every
// materialized node look like it had to be prepared by the scheduler too.
type NodeArtifactFingerprinter interface {
	// Fingerprint materializes the node artifacts and fills in CommandMD5 / ParamsMD5.
	Fingerprint(ctx context.Context, node *types.AnalysisNode) error
}

// PreparerFingerprinter adapts the runtime preparer to the scheduler-side
// fingerprinting contract.
//
// The preparer stays the single source of truth for run.sh / params.json
// rendering, so the scheduler borrows it to predict the artifacts instead of
// re-implementing the rendering rules (and drifting from the dispatcher).
type PreparerFingerprinter struct {
	preparer prepare.NodeRuntimePreparer
}

// NewPreparerFingerprinter wraps a NodeRuntimePreparer.
func NewPreparerFingerprinter(preparer prepare.NodeRuntimePreparer) *PreparerFingerprinter {
	return &PreparerFingerprinter{preparer: preparer}
}

// The container registers this adapter under NodeArtifactFingerprinter via dig.As,
// which is only validated at runtime - keep this compile-time proof in sync.
var _ NodeArtifactFingerprinter = (*PreparerFingerprinter)(nil)

// Fingerprint implements NodeArtifactFingerprinter.
//
// Output cleanup is explicitly disabled: a probe runs for nodes that may well be
// reused as-is, and wiping their output directory would delete the results the
// downstream nodes still reference. The dispatcher cleans the output directory
// when it prepares the node for an actual execution.
func (f *PreparerFingerprinter) Fingerprint(ctx context.Context, node *types.AnalysisNode) error {
	if node == nil {
		return fmt.Errorf("analysis node is nil")
	}
	if f == nil || f.preparer == nil {
		return fmt.Errorf("node runtime preparer is not configured")
	}

	if err := f.preparer.Prepare(prepare.WithSkipCleanOutput(ctx), node); err != nil {
		return fmt.Errorf("prepare dynamic node runtime artifacts failed: %w", err)
	}

	commandMD5, err := fileMD5Hex(node.CommandPath)
	if err != nil {
		return fmt.Errorf("compute run.sh md5 failed: %w", err)
	}
	paramsMD5, err := fileMD5Hex(node.ParamsPath)
	if err != nil {
		return fmt.Errorf("compute params.json md5 failed: %w", err)
	}

	node.CommandMD5 = commandMD5
	node.ParamsMD5 = paramsMD5
	return nil
}

// fileMD5Hex streams a file through MD5 and returns the lowercase hex digest.
func fileMD5Hex(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("file path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := md5.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
