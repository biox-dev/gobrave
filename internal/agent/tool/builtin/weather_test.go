package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

func TestGetWeather(t *testing.T) {
	exec := tool.NewExecutor(tool.NewRegistryWith(All()...))

	res := exec.Execute(context.Background(), tool.Call{
		ID:        "c1",
		Name:      "get_weather",
		Arguments: json.RawMessage(`{"city":"Beijing"}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}

	var out GetWeatherOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal result %q: %v", res.Content, err)
	}
	if out.City != "Beijing" {
		t.Fatalf("city = %q, want Beijing", out.City)
	}
	if out.Unit != "celsius" {
		t.Fatalf("unit = %q, want celsius", out.Unit)
	}
	if out.Temperature <= 0 {
		t.Fatalf("temperature = %v, want > 0", out.Temperature)
	}

	// 确定性：同一城市两次调用返回相同结果。
	res2 := exec.Execute(context.Background(), tool.Call{
		Name:      "get_weather",
		Arguments: json.RawMessage(`{"city":"Beijing"}`),
	})
	if res2.Content != res.Content {
		t.Fatalf("non-deterministic result: %q vs %q", res.Content, res2.Content)
	}
}

func TestGetWeatherFahrenheit(t *testing.T) {
	exec := tool.NewExecutor(tool.NewRegistryWith(GetWeather()))
	res := exec.Execute(context.Background(), tool.Call{
		Name:      "get_weather",
		Arguments: json.RawMessage(`{"city":"Paris","unit":"fahrenheit"}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}

	var out GetWeatherOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Unit != "fahrenheit" {
		t.Fatalf("unit = %q, want fahrenheit", out.Unit)
	}
	if out.Temperature <= 32 {
		t.Fatalf("temperature = %v, want > 32 (fahrenheit)", out.Temperature)
	}
}
