package utils

import (
	"path/filepath"
	"testing"
)

// TestScriptLayout 锁定脚本目录布局：目录口径、主文件名（含 jupyter / qmd）与固定文件名。
// 主文件名曾在 dag/prepare 里有一份会回退成 main.sh 的副本，这里是唯一的期望值来源。
func TestScriptLayout(t *testing.T) {
	const baseDir = "/storage"
	const projectID int64 = 7
	const scriptID = "script-1"

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
		t.Run(tc.scriptType, func(t *testing.T) {
			files := ScriptLayout(baseDir, projectID, tc.scriptType, scriptID)

			wantDir := filepath.Join(baseDir, "data", "7", "pipeline", "script", scriptID)
			if files.Dir != wantDir {
				t.Fatalf("unexpected dir: got=%q want=%q", files.Dir, wantDir)
			}
			if files.MainFile != tc.wantMain {
				t.Fatalf("unexpected main file: got=%q want=%q", files.MainFile, tc.wantMain)
			}
			if files.MainPath != filepath.Join(wantDir, tc.wantMain) {
				t.Fatalf("unexpected main path: got=%q", files.MainPath)
			}
			if files.IOSchema != filepath.Join(wantDir, IOSchemaFileName) {
				t.Fatalf("unexpected io schema path: got=%q", files.IOSchema)
			}
			if files.Readme != filepath.Join(wantDir, ReadmeFileName) {
				t.Fatalf("unexpected readme path: got=%q", files.Readme)
			}
		})
	}
}

// TestWorkflowLayout 锁定工作流目录布局：dag_definition.json / io_schema.json / README.md。
func TestWorkflowLayout(t *testing.T) {
	const baseDir = "/storage"
	const projectID int64 = 7
	const workflowID = "wf-1"

	files := WorkflowLayout(baseDir, projectID, workflowID)

	wantDir := filepath.Join(baseDir, "data", "7", "pipeline", "workflow", workflowID)
	if files.Dir != wantDir {
		t.Fatalf("unexpected dir: got=%q want=%q", files.Dir, wantDir)
	}
	if files.DagDefinition != filepath.Join(wantDir, DagDefinitionFileName) {
		t.Fatalf("unexpected dag definition path: got=%q", files.DagDefinition)
	}
	if files.IOSchema != filepath.Join(wantDir, IOSchemaFileName) {
		t.Fatalf("unexpected io schema path: got=%q", files.IOSchema)
	}
	if files.Readme != filepath.Join(wantDir, ReadmeFileName) {
		t.Fatalf("unexpected readme path: got=%q", files.Readme)
	}
}
