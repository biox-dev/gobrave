package utils

import (
	"path/filepath"
	"strings"
)

// RelPath converts an absolute storage path into the relative form that gets
// persisted in the database.
//
// Persisted storage paths are always relative to storage.base_dir so that
// relocating the storage root only requires copying the directory tree, not
// rewriting database rows. Paths that are already relative (or that live
// outside baseDir, which we do not own) are returned untouched so we never
// silently mangle data.
func RelPath(baseDir, path string) string {
	p := strings.TrimSpace(path)
	if p == "" || !filepath.IsAbs(p) {
		return p
	}
	base := strings.TrimSpace(baseDir)
	if base == "" {
		return p
	}
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return rel
}

// ResolvePath turns a persisted path back into an absolute path.
//
// It is deliberately tolerant: an empty value stays empty and legacy rows that
// still hold an absolute path are returned as-is, so mixed old/new data keeps
// working while the migration is rolling out.
func ResolvePath(baseDir, path string) string {
	p := strings.TrimSpace(path)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	base := strings.TrimSpace(baseDir)
	if base == "" {
		return p
	}
	return filepath.Join(base, p)
}

// RelStoreDir is the base_dir-relative location of a store entry.
func RelStoreDir(id string) string {
	return filepath.Join("store", strings.TrimSpace(id))
}

// AnalysisLayout is the on-disk contract of a single analysis.
//
// Every artifact path an analysis row exposes is derived here, so writers and
// readers cannot drift apart.
type AnalysisLayout struct {
	// Dir is the analysis root directory.
	Dir string
	// ParamsPath is the request params payload written at save time.
	ParamsPath string
	// CommandPath is the generated run.sh path.
	CommandPath string
	// CommandLogPath is the captured run log path.
	CommandLogPath string
	// TraceFile is the nextflow trace log path.
	TraceFile string
	// WorkflowLogFile is the nextflow workflow log path.
	WorkflowLogFile string
	// ExecutorLogFile is the nextflow executor log path.
	ExecutorLogFile string
}

// AnalysisLayoutFor derives the analysis layout from its root directory.
// An empty root yields a zero layout instead of dangling filenames.
func AnalysisLayoutFor(workspaceDir string) AnalysisLayout {
	dir := strings.TrimSpace(workspaceDir)
	if dir == "" {
		return AnalysisLayout{}
	}
	return AnalysisLayout{
		Dir:             dir,
		ParamsPath:      filepath.Join(dir, "params.json"),
		CommandPath:     filepath.Join(dir, "run.sh"),
		CommandLogPath:  filepath.Join(dir, "run.log"),
		TraceFile:       filepath.Join(dir, "trace.log"),
		WorkflowLogFile: filepath.Join(dir, "workflow.log"),
		ExecutorLogFile: filepath.Join(dir, ".nextflow.log"),
	}
}

// NodeLayout is the on-disk contract of a single analysis node.
type NodeLayout struct {
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

// NodeLayoutFor derives the node layout from its root directory.
// An empty root yields a zero layout instead of dangling filenames.
func NodeLayoutFor(workspaceDir string) NodeLayout {
	dir := strings.TrimSpace(workspaceDir)
	if dir == "" {
		return NodeLayout{}
	}
	return NodeLayout{
		Dir:         dir,
		OutputDir:   GetAnalysisNodeOutputDir(dir),
		CacheDir:    GetAnalysisNodeCacheDir(dir),
		ParamsPath:  filepath.Join(dir, "params.json"),
		CommandPath: filepath.Join(dir, "run.sh"),
		LogPath:     filepath.Join(dir, "command.log"),
	}
}
