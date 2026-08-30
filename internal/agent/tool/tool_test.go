package tool

import (
	"context"
	"errors"
	"testing"
	"time"
)

// echo 是一个用于测试的简单工具：把入参文本原样返回。
type echoInput struct {
	Text string `json:"text"`
}

func newEcho() Tool {
	return NewFunc("echo", "echo input text back", Schema("echo input", map[string]any{
		"text": StringProperty("text to echo"),
	}, "text"), func(_ context.Context, in echoInput) (map[string]string, error) {
		return map[string]string{"echoed": in.Text}, nil
	})
}

// failing 是一个始终返回错误的工具。
func newFailing() Tool {
	return NewFunc("failing", "always fails", Schema("", map[string]any{}), func(_ context.Context, in struct{}) (any, error) {
		return nil, errors.New("boom")
	})
}

// panicking 是一个会 panic 的工具。
func newPanicking() Tool {
	return NewFunc("panicking", "panics", Schema("", map[string]any{}), func(_ context.Context, in struct{}) (any, error) {
		panic("kaboom")
	})
}

func TestFuncAdapter(t *testing.T) {
	reg := NewRegistryWith(newEcho())
	exec := NewExecutor(reg)

	res := exec.Execute(context.Background(), Call{
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

func TestExecutorNotFound(t *testing.T) {
	exec := NewExecutor(NewRegistry())
	res := exec.Execute(context.Background(), Call{Name: "missing"})
	if !res.IsError {
		t.Fatalf("expected error result")
	}
	if !errors.Is(res.Err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", res.Err)
	}
}

func TestExecutorInvalidArguments(t *testing.T) {
	exec := NewExecutor(NewRegistryWith(newEcho()))
	res := exec.Execute(context.Background(), Call{Name: "echo", Arguments: []byte(`not-json`)})
	if !res.IsError {
		t.Fatalf("expected error result")
	}
	if !errors.Is(res.Err, ErrInvalidArguments) {
		t.Fatalf("err = %v, want ErrInvalidArguments", res.Err)
	}
}

func TestExecutorToolError(t *testing.T) {
	exec := NewExecutor(NewRegistryWith(newFailing()))
	res := exec.Execute(context.Background(), Call{Name: "failing"})
	if !res.IsError {
		t.Fatalf("expected error result")
	}
	if res.Content != "boom" {
		t.Fatalf("content = %q, want boom", res.Content)
	}
}

func TestExecutorRecoverPanic(t *testing.T) {
	exec := NewExecutor(NewRegistryWith(newPanicking()))
	res := exec.Execute(context.Background(), Call{Name: "panicking"})
	if !res.IsError {
		t.Fatalf("expected error result from panic recovery")
	}
}

func TestExecutorTimeout(t *testing.T) {
	slow := NewFunc("slow", "sleeps", Schema("", map[string]any{}), func(ctx context.Context, _ struct{}) (any, error) {
		select {
		case <-time.After(time.Second):
			return "done", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	exec := NewExecutor(NewRegistryWith(slow), WithMiddlewares(Timeout(10*time.Millisecond)))
	res := exec.Execute(context.Background(), Call{Name: "slow"})
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
