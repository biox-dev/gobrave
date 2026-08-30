package skill

import (
	"context"
	"errors"
	"testing"
	"time"
)

// echo 是一个用于测试的简单技能：把入参文本原样返回。
type echoInput struct {
	Text string `json:"text"`
}

func newEcho() Skill {
	return NewFunc(Manifest{
		Definition: Definition{
			Name:        "echo",
			Description: "echo input text back",
			InputSchema: Schema("echo input", map[string]any{
				"text": StringProperty("text to echo"),
			}, "text"),
		},
	}, func(_ context.Context, in echoInput) (map[string]string, error) {
		return map[string]string{"echoed": in.Text}, nil
	})
}

// failing 是一个始终返回错误的技能。
func newFailing() Skill {
	return NewFunc(Manifest{
		Definition: Definition{Name: "failing", Description: "always fails"},
	}, func(_ context.Context, _ struct{}) (any, error) {
		return nil, errors.New("boom")
	})
}

// panicking 是一个会 panic 的技能。
func newPanicking() Skill {
	return NewFunc(Manifest{
		Definition: Definition{Name: "panicking", Description: "panics"},
	}, func(_ context.Context, _ struct{}) (any, error) {
		panic("kaboom")
	})
}

func TestFuncAdapter(t *testing.T) {
	inv := NewInvoker(NewRegistryWith(newEcho()))

	res := inv.Invoke(context.Background(), Call{
		ID:        "call_1",
		Name:      "echo",
		Arguments: []byte(`{"text":"hello"}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if res.Content != `{"echoed":"hello"}` {
		t.Fatalf("content = %q, want echoed hello", res.Content)
	}
}

func TestInvokerNotFound(t *testing.T) {
	inv := NewInvoker(NewRegistry())
	res := inv.Invoke(context.Background(), Call{Name: "missing"})
	if !res.IsError {
		t.Fatalf("expected error result")
	}
	if !errors.Is(res.Err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", res.Err)
	}
}

func TestInvokerInvalidArguments(t *testing.T) {
	inv := NewInvoker(NewRegistryWith(newEcho()))
	res := inv.Invoke(context.Background(), Call{Name: "echo", Arguments: []byte(`not-json`)})
	if !res.IsError {
		t.Fatalf("expected error result")
	}
	if !errors.Is(res.Err, ErrInvalidArguments) {
		t.Fatalf("err = %v, want ErrInvalidArguments", res.Err)
	}
}

func TestInvokerSkillError(t *testing.T) {
	inv := NewInvoker(NewRegistryWith(newFailing()))
	res := inv.Invoke(context.Background(), Call{Name: "failing"})
	if !res.IsError {
		t.Fatalf("expected error result")
	}
	if res.Content != "boom" {
		t.Fatalf("content = %q, want boom", res.Content)
	}
}

func TestInvokerRecoverPanic(t *testing.T) {
	inv := NewInvoker(NewRegistryWith(newPanicking()))
	res := inv.Invoke(context.Background(), Call{Name: "panicking"})
	if !res.IsError {
		t.Fatalf("expected error result from panic recovery")
	}
}

func TestInvokerTimeout(t *testing.T) {
	slow := NewFunc(Manifest{
		Definition: Definition{Name: "slow"},
	}, func(ctx context.Context, _ struct{}) (any, error) {
		select {
		case <-time.After(time.Second):
			return "done", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	inv := NewInvoker(NewRegistryWith(slow), WithMiddlewares(Timeout(10*time.Millisecond)))
	res := inv.Invoke(context.Background(), Call{Name: "slow"})
	if !res.IsError {
		t.Fatalf("expected timeout error result")
	}
}

func TestRegistryListSorted(t *testing.T) {
	reg := NewRegistryWith(newEcho(), newFailing(), newPanicking())
	names := reg.Names()
	want := []string{"echo", "failing", "panicking"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
	if !reg.Has("echo") || reg.Has("nope") {
		t.Fatalf("Has() mismatch")
	}
	if _, ok := reg.Get("echo"); !ok {
		t.Fatalf("Get(echo) not found")
	}
}

func TestSuccessFailure(t *testing.T) {
	if got := Success("hi").Content; got != "hi" {
		t.Fatalf("Success content = %q", got)
	}
	f := Failure(errors.New("err"))
	if !f.IsError || f.Content != "err" {
		t.Fatalf("Failure = %+v", f)
	}
}

func TestStaticSkill(t *testing.T) {
	s := NewStatic(Manifest{
		Definition:   Definition{Name: "guide", Description: "a guide"},
		Instructions: "do this",
	})
	if s.Definition().Name != "guide" {
		t.Fatalf("name = %q", s.Definition().Name)
	}
	res, err := s.Invoke(context.Background(), nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Content != "do this" {
		t.Fatalf("content = %q", res.Content)
	}
}
