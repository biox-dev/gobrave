package tool

import (
	"context"
	"slices"
	"testing"
)

// newNamed 构造一个只关心名字的测试工具。
func newNamed(name string) Tool {
	return NewFunc(name, "test tool "+name, Schema("", map[string]any{}),
		func(_ context.Context, _ struct{}) (any, error) { return nil, nil })
}

func newFilterTestRegistry() *Registry {
	return NewRegistryWith(newNamed("gamma"), newNamed("alpha"), newNamed("beta"))
}

func toolNames(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Definition().Name)
	}
	return out
}

func TestRegistryFilterEmptyReturnsAll(t *testing.T) {
	reg := newFilterTestRegistry()

	for _, names := range [][]string{nil, {}} {
		got := toolNames(reg.Filter(names))
		want := []string{"alpha", "beta", "gamma"}
		if !slices.Equal(got, want) {
			t.Fatalf("Filter(%v) = %v, want %v", names, got, want)
		}
	}
}

func TestRegistryFilterWhitelist(t *testing.T) {
	reg := newFilterTestRegistry()

	got := toolNames(reg.Filter([]string{"gamma", " alpha ", "alpha", "missing", ""}))
	want := []string{"alpha", "gamma"}
	if !slices.Equal(got, want) {
		t.Fatalf("Filter whitelist = %v, want %v", got, want)
	}
}

func TestRegistryMissing(t *testing.T) {
	reg := newFilterTestRegistry()

	got := reg.Missing([]string{"gamma", "nope", "nope", " ", "alpha"})
	want := []string{"nope"}
	if !slices.Equal(got, want) {
		t.Fatalf("Missing = %v, want %v", got, want)
	}
	if got := reg.Missing(nil); got != nil {
		t.Fatalf("Missing(nil) = %v, want nil", got)
	}
}
