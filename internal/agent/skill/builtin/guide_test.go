package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/skill"
)

func TestReviewGuide(t *testing.T) {
	s := ReviewGuide()

	if got := s.Definition().Name; got != "review_guide" {
		t.Fatalf("name = %q, want review_guide", got)
	}

	// Static 型技能的指令正文应为非空 markdown。
	instr := s.Instructions()
	if !strings.Contains(instr, "正确性") || !strings.Contains(instr, "测试") {
		t.Fatalf("instructions missing expected sections: %q", instr)
	}

	// Invoke 直接返回指令正文。
	res, err := s.Invoke(context.Background(), nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if res.Content != instr {
		t.Fatalf("invoke content != instructions")
	}
}

func TestAllContainsStatic(t *testing.T) {
	reg := skill.NewRegistryWith(All()...)
	if !reg.Has("review_guide") {
		t.Fatalf("All() 未注册 review_guide 静态技能")
	}
	names := reg.Names()
	want := []string{"echo", "get_weather", "review_guide"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}
