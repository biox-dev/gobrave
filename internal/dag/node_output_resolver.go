package dag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/logger"
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
			// Missing is the documented non-error case: a node may legitimately
			// produce no outputs.json, and the completion path retries this read
			// while the container's last write becomes visible. It is therefore
			// logged at debug level - an ERROR here printed one line per retry and
			// buried the real completion outcome. The outcome is stated once, by
			// the coordinator, when declared outputs are still missing when the
			// grace window expires.
			logger.Debugf(context.Background(), "output resolve: %s not present yet", path)
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

// nodeOutputDir locates the directory a node writes its outputs into,
// preferring the explicitly hydrated output dir and falling back to
// <workspace>/output.
func nodeOutputDir(node *types.AnalysisNode) (string, bool) {
	if node == nil {
		return "", false
	}
	if outputDir := strings.TrimSpace(node.OutputDir); outputDir != "" {
		return outputDir, true
	}
	if workspaceDir := strings.TrimSpace(node.WorkspaceDir); workspaceDir != "" {
		return filepath.Join(workspaceDir, "output"), true
	}
	return "", false
}

// nodeOutputsPath locates the outputs.json file for a node, preferring the
// explicitly hydrated output dir and falling back to <workspace>/output.
func nodeOutputsPath(node *types.AnalysisNode) (string, bool) {
	dir, ok := nodeOutputDir(node)
	if !ok {
		return "", false
	}
	return filepath.Join(dir, nodeOutputsFileName), true
}

// outputProbeMaxEntries bounds how much of an output directory listing is spent
// on one log line.
const outputProbeMaxEntries = 20

// outputProbeDetail renders, in one line, what this process can see in an output
// directory: each declared file's stat result before the directory is listed, the
// listing itself, and the stat result again afterwards.
//
// The order is the point. The caller reaches this point because a read of
// outputs.json failed, and the question worth answering is whether the file is
// absent or merely invisible from here: the analysis workspace is an NFS mount
// shared by this process and by the DAG node containers, and Prepare deletes a
// node's previous outputs just before it runs, which is what leaves this process
// holding a cached "does not exist" for outputs.json. Stat-ing before the listing
// can observe that cached answer, and stat-ing again after it shows the answer
// being corrected: a name reported missing before the readdir and present after it
// was on the server all along. Those names are returned separately, as
// stat_vs_readdir_disagree_on.
//
// Every mtime is logged with its distance from this process's clock. A negative
// distance is expected here: the timestamps are stamped by the NFS server, whose
// clock is ahead of this host's (measured at ~63s), and the magnitude is the
// offset needed to place the write on this timeline.
func outputProbeDetail(dir string, wanted ...string) (string, []string) {
	parts := make([]string, 0, len(wanted)+2)

	visibleBeforeListing := make(map[string]bool, len(wanted))
	before := make([]string, 0, len(wanted))
	for _, name := range wanted {
		text, ok := statOutputFile(dir, name)
		visibleBeforeListing[name] = ok
		before = append(before, text)
	}
	parts = append(parts, fmt.Sprintf("stat_before_readdir=[%s]", strings.Join(before, " ")))

	listed := make(map[string]bool, len(wanted))
	entries, err := os.ReadDir(dir)
	if err != nil {
		parts = append(parts, fmt.Sprintf("readdir(%s)=%v", dir, err))
	} else {
		names := make([]string, 0, len(entries))
		for i, entry := range entries {
			if i >= outputProbeMaxEntries {
				names = append(names, fmt.Sprintf("...%d_more", len(entries)-outputProbeMaxEntries))
				break
			}
			listed[entry.Name()] = true
			info, infoErr := entry.Info()
			if infoErr != nil {
				names = append(names, fmt.Sprintf("%s(info_error=%v)", entry.Name(), infoErr))
				continue
			}
			names = append(names, fmt.Sprintf("%s(size=%d,mtime=%s,mtime_vs_now=%s)",
				entry.Name(), info.Size(), info.ModTime().Format(time.RFC3339Nano),
				time.Since(info.ModTime()).Round(time.Millisecond)))
		}
		parts = append(parts, fmt.Sprintf("readdir=[%s]", strings.Join(names, " ")))
	}

	hidden := make([]string, 0, len(wanted))
	after := make([]string, 0, len(wanted))
	for _, name := range wanted {
		if visibleBeforeListing[name] {
			continue
		}
		if listed[name] {
			hidden = append(hidden, name)
		}
		text, _ := statOutputFile(dir, name)
		after = append(after, text)
	}
	if len(after) > 0 {
		parts = append(parts, fmt.Sprintf("stat_after_readdir=[%s]", strings.Join(after, " ")))
	}
	return strings.Join(parts, " "), hidden
}

// statOutputFile stats <dir>/<name> and renders the result as "name=..." for a log
// line, reporting whether the file was readable.
func statOutputFile(dir, name string) (string, bool) {
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return fmt.Sprintf("%s=%v", name, err), false
	}
	return fmt.Sprintf("%s=ok(size=%d,mtime=%s,mtime_vs_now=%s)", name, info.Size(),
		info.ModTime().Format(time.RFC3339Nano),
		time.Since(info.ModTime()).Round(time.Millisecond)), true
}

// outputDirMountInfo returns the mount entry backing dir, so a failure log also
// carries the cache options (actimeo/acdirmin/acdirmax on NFS) that decide how
// long a file written by another client can stay invisible here.
func outputDirMountInfo(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return fmt.Sprintf("mounts_unavailable=%v", err)
	}

	bestMount, bestEntry := "", ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		mountPoint := strings.ReplaceAll(fields[1], `\040`, " ")
		if mountPoint != abs && !strings.HasPrefix(abs, strings.TrimSuffix(mountPoint, "/")+"/") {
			continue
		}
		if len(mountPoint) < len(bestMount) {
			continue
		}
		bestMount, bestEntry = mountPoint, strings.Join(fields[:4], " ")
	}
	if bestMount == "" {
		return ""
	}
	return "mount=" + bestEntry
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
