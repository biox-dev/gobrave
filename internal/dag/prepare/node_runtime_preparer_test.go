package prepare

import "testing"

func TestRenderShellTemplate(t *testing.T) {
	tmpl := "echo {{ sample_id }} {{ meta.file_name }} {{ meta_file_name }}"
	params := map[string]any{
		"sample_id": "S1",
		"meta": map[string]any{
			"file_name": "input.fastq",
		},
	}

	rendered, err := renderShellTemplate(tmpl, params)
	if err != nil {
		t.Fatalf("render shell template failed: %v", err)
	}

	want := "echo S1 input.fastq input.fastq"
	if rendered != want {
		t.Fatalf("unexpected rendered template: got=%q want=%q", rendered, want)
	}
}

func TestRenderShellTemplateUndefinedVariableUsesPongo2Default(t *testing.T) {
	tmpl := "echo {{ missing.key }}"
	rendered, err := renderShellTemplate(tmpl, map[string]any{"sample_id": "S1"})
	if err != nil {
		t.Fatalf("expected no error for undefined variable with pongo2 default behavior: %v", err)
	}
	if rendered != "echo " {
		t.Fatalf("unexpected rendered template: got=%q want=%q", rendered, "echo ")
	}
}

func TestMainFileByScriptType(t *testing.T) {
	cases := []struct {
		name       string
		scriptType string
		want       string
	}{
		{name: "r", scriptType: "r", want: "main.R"},
		{name: "python", scriptType: "python", want: "main.py"},
		{name: "shell", scriptType: "shell", want: "main.sh"},
		{name: "jupyter fallback", scriptType: "jupyter", want: "main.sh"},
		{name: "empty fallback", scriptType: "", want: "main.sh"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mainFileByScriptType(tc.scriptType); got != tc.want {
				t.Fatalf("unexpected main file: got=%q want=%q", got, tc.want)
			}
		})
	}
}
