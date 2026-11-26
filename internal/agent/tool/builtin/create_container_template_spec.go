package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateContainerTemplateSpecToolName 是「创建共享运行配置」工具名。
const CreateContainerTemplateSpecToolName = "create_container_template_spec"

// DefaultContainerTemplatePort 是未指定 port 时使用的容器端口（与 DB 默认值一致）。
const DefaultContainerTemplatePort = 8787

// CreateContainerTemplateSpecInput 是 create_container_template_spec 工具的入参。
//
// 它只描述「怎么跑」：命令、资源、端口、环境变量、挂载、标签等，不含镜像。
// 需要让模板可运行起来时，紧接着调用 create_container_template_definition 把该配置
// 绑定到一个具体镜像上。
type CreateContainerTemplateSpecInput struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Command     string  `json:"command"`
	CPU         float64 `json:"cpu"`
	Memory      int64   `json:"memory"`
	Port        int     `json:"port"`
	AppType     string  `json:"app_type"`

	Env                  map[string]any   `json:"env"`
	Mounts               []map[string]any `json:"mounts"`
	SchedulingConstraint map[string]any   `json:"scheduling_constraint"`
	Labels               map[string]any   `json:"labels"`
	ChangeUID            bool             `json:"change_uid"`
}

// CreateContainerTemplateSpec 返回「创建共享运行配置」工具。
//
// 行为：
//   - name 必填；其余字段可选；
//   - port 未提供（0）时按 DefaultContainerTemplatePort（8787）写入；
//   - env / mounts / scheduling_constraint / labels 直接按 JSON 结构落库；
//   - 通过注入的 ContainerTemplateSpecWriter 写库，回传带新主键的记录。
//
// 该工具会创建业务数据（写库），因此声明 write 授权：上层在执行前会先走权限门禁。
func CreateContainerTemplateSpec(writer runtimecontext.ContainerTemplateSpecWriter) tool.Tool {
	return tool.NewFunc(
		CreateContainerTemplateSpecToolName,
		"Create a SHARED container run configuration (ContainerTemplateSpec): the command, CPU/memory, port, env, mounts, labels and scheduling constraints used to run a container image. This row does NOT bind any image — to get a runnable container template call create_container_template_definition with the returned spec_id and an image_name. The created spec_id can also be reused by multiple images. `name` is required; `port` defaults to 8787.",
		tool.Schema("创建共享运行配置的入参", map[string]any{
			"name":                  tool.StringProperty("运行配置名称（必填），用于在列表中区分不同的「怎么跑」配置。"),
			"description":           tool.StringProperty("运行配置说明（可选）。"),
			"command":               tool.StringProperty("容器启动命令（可选）；为空时使用镜像默认入口。"),
			"cpu":                   tool.NumberProperty("CPU 配额（核数，可为小数）；0 表示不限制。"),
			"memory":                tool.IntegerProperty("内存配额（字节）；0 表示不限制。"),
			"port":                  tool.IntegerProperty("容器内应用端口（默认 8787），网关按该端口转发流量。"),
			"app_type":              tool.StringProperty("应用类型（可选），如 rstudio / code-server / jupyter。"),
			"env":                   containerTemplateEnvSchema(),
			"mounts":                containerTemplateMountsSchema(),
			"scheduling_constraint": containerTemplateSchedulingConstraintSchema(),
			"labels":                containerTemplateLabelsSchema(),
			"change_uid":            tool.BooleanProperty("是否在容器内把运行用户切换为宿主用户（默认 false）。"),
		}, "name"),
		func(ctx context.Context, in CreateContainerTemplateSpecInput) (ContainerTemplateSpecWriteOutput, error) {
			name := strings.TrimSpace(in.Name)
			if name == "" {
				return ContainerTemplateSpecWriteOutput{}, fmt.Errorf("%s: name is required", CreateContainerTemplateSpecToolName)
			}
			if writer == nil {
				return ContainerTemplateSpecWriteOutput{}, fmt.Errorf("%s: container template spec writer is not available in this runtime", CreateContainerTemplateSpecToolName)
			}

			port := in.Port
			if port == 0 {
				port = DefaultContainerTemplatePort
			}

			record, err := writer.CreateContainerTemplateSpec(ctx, runtimecontext.ContainerTemplateSpecCreateInput{
				Name:                 name,
				Description:          strings.TrimSpace(in.Description),
				Command:              strings.TrimSpace(in.Command),
				CPU:                  in.CPU,
				Memory:               in.Memory,
				Port:                 port,
				AppType:              strings.TrimSpace(in.AppType),
				Env:                  in.Env,
				Mounts:               in.Mounts,
				SchedulingConstraint: in.SchedulingConstraint,
				Labels:               in.Labels,
				ChangeUID:            in.ChangeUID,
			})
			if err != nil {
				return ContainerTemplateSpecWriteOutput{Error: err.Error()}, nil
			}
			return ContainerTemplateSpecWriteOutput{ContainerTemplateSpecRecord: record}, nil
		},
	).WithPermission("write", "", "创建容器模板共享运行配置")
}
