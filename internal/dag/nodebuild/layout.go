package nodebuild

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/utils"
)

// WorkspaceLayout is the on-disk contract of a single analysis node.
//
// It is intentionally complete: every artifact path a node row stores comes
// from here, so the runtime preparer, the fingerprint probe and the completion
// resolver all agree on where files live.
type WorkspaceLayout struct {
	// Dir is the node root directory.
	Dir string
	// OutputDir receives produced artifacts.
	OutputDir string
	// CacheDir holds intermediate cache artifacts.
	CacheDir string
	// ParamsPath is the generated params.json path.
	ParamsPath string
	// CommandPath is the generated run.sh path.
	CommandPath string
	// LogPath is the captured command log path.
	LogPath string
}

// LayoutFor allocates the workspace layout below the analysis output directory.
//
// The node record primary key doubles as the directory name so the on-disk tree
// stays traceable from the database row.
func LayoutFor(analysisOutputDir string, nodeRecordID int64) WorkspaceLayout {
	dir := filepath.Join(analysisOutputDir, strconv.FormatInt(nodeRecordID, 10))
	return LayoutFromWorkspaceDir(dir)
}

// LayoutFromWorkspaceDir derives the full layout from an already known root.
func LayoutFromWorkspaceDir(workspaceDir string) WorkspaceLayout {
	return WorkspaceLayout{
		Dir:         workspaceDir,
		OutputDir:   utils.GetAnalysisNodeOutputDir(workspaceDir),
		CacheDir:    utils.GetAnalysisNodeCacheDir(workspaceDir),
		ParamsPath:  filepath.Join(workspaceDir, "params.json"),
		CommandPath: filepath.Join(workspaceDir, "run.sh"),
		LogPath:     filepath.Join(workspaceDir, "command.log"),
	}
}

// IsZero reports whether no layout could be derived, which happens when the
// analysis has no output directory configured yet.
func (l WorkspaceLayout) IsZero() bool { return strings.TrimSpace(l.Dir) == "" }

// ResolveLayout picks the workspace for a new node.
//
// Precedence:
//  1. an explicit override, used by callers that pre-allocate a workspace;
//  2. analysis.output_dir/<node record id>, the canonical layout;
//  3. empty, when the analysis has no output directory yet - the caller is then
//     responsible for filling the paths in later (see v3's path defaults).
func ResolveLayout(analysisOutputDir string, workspaceOverride string, nodeRecordID int64) WorkspaceLayout {
	if override := strings.TrimSpace(workspaceOverride); override != "" {
		return LayoutFromWorkspaceDir(override)
	}
	if strings.TrimSpace(analysisOutputDir) == "" {
		return WorkspaceLayout{}
	}
	return LayoutFor(analysisOutputDir, nodeRecordID)
}
