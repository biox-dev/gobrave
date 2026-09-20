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
	Resolve(node *types.AnalysisNode, candidateOutputs map[string]any) (map[string]any, []string)
}

// fileSystemNodeOutputResolver reads node outputs from the node output
// directory's outputs.json file.
type fileSystemNodeOutputResolver struct{}

func newFileSystemNodeOutputResolver() nodeOutputResolver {
	return &fileSystemNodeOutputResolver{}
}

// Resolve returns the container-produced outputs (candidateOutputs) merged with
// the contents of <output_dir>/outputs.json, which takes precedence. A missing
// outputs.json is not an error; a malformed one is reported through the returned
// error list.
func (r *fileSystemNodeOutputResolver) Resolve(node *types.AnalysisNode, candidateOutputs map[string]any) (map[string]any, []string) {
	outputs := cloneMap(candidateOutputs)

	path, ok := nodeOutputsPath(node)
	if !ok {
		return outputs, nil
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return outputs, []string{fmt.Sprintf("%s not found", path)}
		}
		return outputs, []string{fmt.Sprintf("read %s failed: %v", nodeOutputsFileName, err)}
	}

	payload := map[string]any{}
	if err := json.Unmarshal(buf, &payload); err != nil {
		return outputs, []string{fmt.Sprintf("invalid %s: %v", nodeOutputsFileName, err)}
	}
	for handle, value := range payload {
		outputs[handle] = value
	}
	return outputs, nil
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
