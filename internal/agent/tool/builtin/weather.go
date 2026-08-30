package builtin

import (
	"context"
	"hash/fnv"
	"math"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetWeatherInput 是 get_weather 工具的入参。
type GetWeatherInput struct {
	City string `json:"city"` // 城市名（必填）
	Unit string `json:"unit"` // 温度单位：celsius / fahrenheit，默认 celsius
}

// GetWeatherOutput 是 get_weather 工具的返回。
type GetWeatherOutput struct {
	City        string  `json:"city"`
	Temperature float64 `json:"temperature"`
	Unit        string  `json:"unit"`
	Condition   string  `json:"condition"`
	Humidity    int     `json:"humidity"`
}

// GetWeather 返回一个「获取天气」工具（当前返回模拟数据，供演示 / 联调）。
//
// 同一城市返回确定的温度，便于测试断言。后续接入真实天气服务时，只需替换
// mockWeather 的内部实现，工具定义与签名保持不变。
func GetWeather() tool.Tool {
	return tool.NewFunc("get_weather", "查询指定城市的天气（当前为模拟数据）",
		tool.Schema("获取天气的入参", map[string]any{
			"city": tool.StringProperty("城市名称"),
			"unit": tool.StringProperty("温度单位，celsius 或 fahrenheit，默认 celsius"),
		}, "city"),
		func(_ context.Context, in GetWeatherInput) (GetWeatherOutput, error) {
			return mockWeather(in), nil
		})
}

// conditions 是模拟的天气状况集合。
var conditions = []string{"sunny", "cloudy", "rainy", "windy", "foggy"}

// mockWeather 生成确定性的模拟天气数据。
func mockWeather(in GetWeatherInput) GetWeatherOutput {
	city := strings.TrimSpace(in.City)
	if city == "" {
		city = "unknown"
	}
	unit := normalizeUnit(in.Unit)

	h := fnv.New32a()
	_, _ = h.Write([]byte(city))
	seed := h.Sum32()

	celsius := 5 + float64(seed%300)/10.0 // 5.0 ~ 34.9 ℃
	temp := round1(celsius)
	if unit == "fahrenheit" {
		temp = round1(celsius*9/5 + 32)
	}

	return GetWeatherOutput{
		City:        city,
		Temperature: temp,
		Unit:        unit,
		Condition:   conditions[seed%uint32(len(conditions))],
		Humidity:    40 + int(seed%50), // 40% ~ 89%
	}
}

// normalizeUnit 规范化温度单位，非法值回退到 celsius。
func normalizeUnit(unit string) string {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "f", "fahrenheit":
		return "fahrenheit"
	default:
		return "celsius"
	}
}

// round1 保留一位小数。
func round1(f float64) float64 {
	return math.Round(f*10) / 10
}
