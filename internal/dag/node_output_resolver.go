package dag

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
)

// nodeOutputsFileName is the file a node writes its resolved outputs into,
// located inside the node output directory.
const nodeOutputsFileName = "outputs.json"

// nodeOutputResolver resolves the outputs produced by a finished node.
type nodeOutputResolver interface {
	// Resolve merges candidateOutputs with the contents of
	// <output_dir>/outputs.json (the file wins).
	//
	// The second return value reports whether outputs.json was absent. A missing
	// file is NOT an error: a node without declared output_patterns legitimately
	// produces no outputs.json, and a container process may flush the file a
	// moment after its exit was observed. Callers decide how to react; only a
	// read or parse failure is returned through the error list.
	Resolve(node *types.AnalysisNode, candidateOutputs map[string]any) (map[string]any, bool, []string)
}

// fileSystemNodeOutputResolver reads node outputs from the node output
// directory's outputs.json file.
type fileSystemNodeOutputResolver struct{}

func newFileSystemNodeOutputResolver() nodeOutputResolver {
	return &fileSystemNodeOutputResolver{}
}

// Resolve returns the container-produced outputs (candidateOutputs) merged with
// the contents of <output_dir>/outputs.json, which takes precedence. A missing
// outputs.json is not an error (it is signalled through the boolean result);
// only a malformed or unreadable one is reported through the returned error list.
func (r *fileSystemNodeOutputResolver) Resolve(node *types.AnalysisNode, candidateOutputs map[string]any) (map[string]any, bool, []string) {
	outputs := cloneMap(candidateOutputs)

	path, ok := nodeOutputsPath(node)
	if !ok {
		return outputs, true, nil
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return outputs, true, nil
		}
		return outputs, false, []string{fmt.Sprintf("read %s failed: %v", path, err)}
	}

	payload := map[string]any{}
	if err := json.Unmarshal(buf, &payload); err != nil {
		return outputs, false, []string{fmt.Sprintf("invalid %s: %v", nodeOutputsFileName, err)}
	}
	for handle, value := range payload {
		outputs[handle] = value
	}
	return outputs, false, nil
}

// nodeOutputsPath locates the outputs.json file for a node, preferring the
// explicitly hydrated output dir and falling back to <workspace>/output.
func nodeOutputsPath(node *types.AnalysisNode) (string, bool) {
	if node == nil {
		return "", false
	}
	if outputDir := strings.TrimSpace(node.OutputDir); outputDir != "" {
		return filepath.Join(outputDir, nodeOutputsFileName), true
	}
	if workspaceDir := strings.TrimSpace(node.WorkspaceDir); workspaceDir != "" {
		return filepath.Join(workspaceDir, "output", nodeOutputsFileName), true
	}
	return "", false
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(src))
	for k, v := range src {
		cloned[k] = v
	}
	return cloned
}
