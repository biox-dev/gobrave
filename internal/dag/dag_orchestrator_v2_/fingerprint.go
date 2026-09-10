package orchestratorv2

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

// Fingerprinter fills the CommandMD5 / ParamsMD5 fields of a node by preparing
// its runtime artifacts and hashing them.
//
// It is an interface (rather than a bare function) so cache policies stay
// testable without touching the filesystem.
type Fingerprinter interface {
	// Fingerprint prepares the node artifacts and stores their digests on node.
	Fingerprint(ctx context.Context, node *types.AnalysisNode) error
}

// PreparerFingerprinter is the production Fingerprinter backed by the DAG
// runtime preparer.
//
// The preparer regenerates params.json and run.sh from the current node
// payload, which is exactly the input of the cache comparison. Output cleanup
// is skipped so probing an already finished node never destroys its results.
type PreparerFingerprinter struct {
	preparer prepare.NodeRuntimePreparer
}

// NewPreparerFingerprinter wraps a NodeRuntimePreparer.
func NewPreparerFingerprinter(preparer prepare.NodeRuntimePreparer) *PreparerFingerprinter {
	return &PreparerFingerprinter{preparer: preparer}
}

// Fingerprint implements Fingerprinter.
func (f *PreparerFingerprinter) Fingerprint(ctx context.Context, node *types.AnalysisNode) error {
	if node == nil {
		return fmt.Errorf("analysis node is nil")
	}
	if f == nil || f.preparer == nil {
		return fmt.Errorf("node runtime preparer is not configured")
	}

	// The dispatcher re-prepares the node right before execution, so skipping
	// the output cleanup here is safe and keeps cached results intact.
	prepareCtx := prepare.WithSkipCleanOutput(ctx)
	if err := f.preparer.Prepare(prepareCtx, node); err != nil {
		return fmt.Errorf("prepare node runtime artifacts failed: %w", err)
	}

	commandMD5, err := fileMD5Hex(node.CommandPath)
	if err != nil {
		return fmt.Errorf("hash run script failed: %w", err)
	}
	paramsMD5, err := fileMD5Hex(node.ParamsPath)
	if err != nil {
		return fmt.Errorf("hash params payload failed: %w", err)
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
