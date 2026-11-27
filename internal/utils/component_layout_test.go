package utils

import (
	"path/filepath"
	"testing"
)

// TestScriptMainFileName 锁定脚本主文件名映射（含 jupyter / qmd）。
// 主文件名曾在 dag/prepare 里有一份会回退成 main.sh 的副本，这里是唯一的期望值来源。
func TestScriptMainFileName(t *testing.T) {
	cases := []struct {
		scriptType string
		wantMain   string
	}{
		{scriptType: "r", wantMain: "main.R"},
		{scriptType: "python", wantMain: "main.py"},
		{scriptType: "shell", wantMain: "main.sh"},
		{scriptType: "jupyter", wantMain: "main.ipynb"},
		{scriptType: "qmd", wantMain: "main.qmd"},
		{scriptType: "", wantMain: "main.R"},
	}

	for _, tc := range cases {
		if got := ScriptMainFileName(tc.scriptType); got != tc.wantMain {
			t.Fatalf("script type %q: unexpected main file name: got=%q want=%q", tc.scriptType, got, tc.wantMain)
		}
	}
}

// TestScriptFilesLayout 锁定脚本目录内各文件的路径口径（逐文件函数）。
func TestScriptFilesLayout(t *testing.T) {
	const baseDir = "/storage"
	const projectID int64 = 7
	const scriptID = "script-1"
	const scriptType = "python"

	wantDir := filepath.Join(baseDir, "data", "7", "pipeline", "script", scriptID)
	if got := GetScriptFileDir(baseDir, projectID, scriptID); got != wantDir {
		t.Fatalf("unexpected dir: got=%q want=%q", got, wantDir)
	}
	if got := ScriptMainFilePath(baseDir, projectID, scriptType, scriptID); got != filepath.Join(wantDir, "main.py") {
		t.Fatalf("unexpected main path: got=%q", got)
	}
	if got := ScriptIOSchemaPath(baseDir, projectID, scriptID); got != filepath.Join(wantDir, IOSchemaFileName) {
		t.Fatalf("unexpected io schema path: got=%q", got)
	}
	if got := ScriptReadmePath(baseDir, projectID, scriptID); got != filepath.Join(wantDir, ReadmeFileName) {
		t.Fatalf("unexpected readme path: got=%q", got)
	}
}

// TestWorkflowFilesLayout 锁定工作流目录内各文件的路径口径（逐文件函数）。
func TestWorkflowFilesLayout(t *testing.T) {
	const baseDir = "/storage"
	const projectID int64 = 7
	const workflowID = "wf-1"

	wantDir := filepath.Join(baseDir, "data", "7", "pipeline", "workflow", workflowID)
	if got := GetWorkflowFileDir(baseDir, projectID, workflowID); got != wantDir {
		t.Fatalf("unexpected dir: got=%q want=%q", got, wantDir)
	}
	if got := WorkflowDagDefinitionPath(baseDir, projectID, workflowID); got != filepath.Join(wantDir, DagDefinitionFileName) {
		t.Fatalf("unexpected dag definition path: got=%q", got)
	}
	if got := WorkflowIOSchemaPath(baseDir, projectID, workflowID); got != filepath.Join(wantDir, IOSchemaFileName) {
		t.Fatalf("unexpected io schema path: got=%q", got)
	}
	if got := WorkflowReadmePath(baseDir, projectID, workflowID); got != filepath.Join(wantDir, ReadmeFileName) {
		t.Fatalf("unexpected readme path: got=%q", got)
	}
}
