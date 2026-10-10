package compiler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const defaultDebugAnalysisID = "2388014a-5542-485c-bda9-a431f88e6c6b"
const baseDir = "/data2/brave_analysis_workspace/dag"

type debugParamsPayload struct {
	AnalysisID          int64          `json:"analysis_id"`
	ParseAnalysisResult map[string]any `json:"parse_analysis_result"`
	DagDefinition       map[string]any `json:"dag_definition"`
}

func TestBuildRuntimeTasks_DebugFromLocalParams(t *testing.T) {
	// analysisID := os.Getenv("BRAVE_DAG_ANALYSIS_ID")
	// if analysisID == "" {
	// 	analysisID = defaultDebugAnalysisID
	// }
	analysisID := int64(1)
	// baseDir := os.Getenv("BRAVE_DAG_BASE_DIR")
	// if baseDir == "" {
	// 	// home, err := os.UserHomeDir()
	// 	// if err != nil {
	// 	// 	t.Fatalf("resolve home dir: %v", err)
	// 	// }
	// 	// baseDir = filepath.Join(home, ".brave", "dag")
	// 	baseDir = baseDir
	// }

	paramsPath := filepath.Join(baseDir, fmt.Sprintf("%d", analysisID), "params.json")
	raw, err := os.ReadFile(paramsPath)
	if err != nil {
		t.Skipf("skip debug test: cannot read params file %s: %v", paramsPath, err)
	}

	var payload debugParamsPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal %s: %v", paramsPath, err)
	}

	if payload.AnalysisID != 0 {
		analysisID = payload.AnalysisID
	}

	runtimeDAG, err := BuildRuntimeTasks(analysisID, payload.ParseAnalysisResult, map[string]any{}, payload.DagDefinition)
	if err != nil {
		t.Fatalf("BuildRuntimeTasks failed: %v", err)
	}

	// 将 parseAnalysisResult+dagDefinition 写入json文件

	dagDebug := filepath.Join(baseDir, fmt.Sprintf("%d", analysisID))

	resultPath := filepath.Join(dagDebug, "runtime_dag.json")
	f, err := os.Create(resultPath)
	if err == nil {
		encoder := json.NewEncoder(f)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(runtimeDAG)
		f.Close()
	}
	fmt.Printf("runtimeDAG: %s", resultPath)

}
