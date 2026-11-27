package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateContainerTemplateSpecToolName 是「更新共享运行配置」工具名。
const UpdateContainerTemplateSpecToolName = "update_container_template_spec"

// UpdateContainerTemplateSpecInput 是 update_container_template_spec 工具的入参。
//
// 指针字段为 nil 表示不修改；spec_id 必填。已绑定该配置的所有镜像都会沿用新值。
type UpdateContainerTemplateSpecInput struct {
	SpecID      string   `json:"spec_id"`
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	Command     *string  `json:"command"`
	CPU         *float64 `json:"cpu"`
	Memory      *int64   `json:"memory"`
	Port        *int     `json:"port"`
	AppType     *string  `json:"app_type"`

	Env                  map[string]any   `json:"env"`
	Mounts               []map[string]any `json:"mounts"`
	SchedulingConstraint map[string]any   `json:"scheduling_constraint"`
	Labels               map[string]any   `json:"labels"`
	ChangeUID            *bool            `json:"change_uid"`
}

// UpdateContainerTemplateSpec 返回「更新共享运行配置」工具。
//
// 行为：
//   - spec_id 必填；至少提供一个要修改的字段；
//   - 只覆盖本次给出的字段，未给出的字段保持数据库原值；
//   - 回传更新后的记录。
//
// 该工具会修改业务数据（写库），且会同时影响所有绑定到该配置的镜像，
// 因此提示模型「非必要不修改、修改前需用户确认」；同时声明 write 授权，
// 上层在执行前会先走权限门禁。
func UpdateContainerTemplateSpec(writer runtimecontext.ContainerTemplateSpecWriter) tool.Tool {
	return tool.NewFunc(
		UpdateContainerTemplateSpecToolName,
		"Update a SHARED container run configuration (ContainerTemplateSpec) by spec_id: command, CPU/memory, port, env, mounts, labels, scheduling constraints and change_uid. Only fields you pass are changed; every image bound to this spec (ContainerTemplateDefinition rows) keeps using it, so already created scripts / sessions pick up the new values on next run. This affects all bound images, so do not call it unless the change is really necessary, and confirm the intent with the user before making any change. 运行配置的命令 / 资源 / 端口 / 环境变量属于这一层，镜像名请改用 update_container_template_definition；非必要不修改，修改前需先与用户确认。",
		tool.Schema("更新共享运行配置的入参", map[string]any{
			"spec_id":               tool.StringProperty("共享运行配置 ID（go_container_template_spec 主键，必填，字符串以保精度）。"),
			"name":                  tool.StringProperty("新的运行配置名称；不传表示不修改。"),
			"description":           tool.StringProperty("新的运行配置说明；不传表示不修改。"),
			"command":               tool.StringProperty("新的容器启动命令；不传表示不修改。"),
			"cpu":                   tool.NumberProperty("新的 CPU 配额；不传表示不修改。"),
			"memory":                tool.IntegerProperty("新的内存配额（字节）；不传表示不修改。"),
			"port":                  tool.IntegerProperty("新的容器内应用端口；不传表示不修改。"),
			"app_type":              tool.StringProperty("新的应用类型；不传表示不修改。"),
			"env":                   containerTemplateEnvSchema(),
			"mounts":                containerTemplateMountsSchema(),
			"scheduling_constraint": containerTemplateSchedulingConstraintSchema(),
			"labels":                containerTemplateLabelsSchema(),
			"change_uid":            tool.BooleanProperty("是否切换为宿主用户运行；不传表示不修改。"),
		}, "spec_id"),
		func(ctx context.Context, in UpdateContainerTemplateSpecInput) (ContainerTemplateSpecWriteOutput, error) {
			specID, err := parseContainerTemplateSpecID(in.SpecID)
			if err != nil {
				return ContainerTemplateSpecWriteOutput{}, wrapToolNameError(UpdateContainerTemplateSpecToolName, err)
			}
			if !hasContainerTemplateSpecUpdate(in) {
				return ContainerTemplateSpecWriteOutput{}, fmt.Errorf("%s: at least one field to update must be provided", UpdateContainerTemplateSpecToolName)
			}
			if writer == nil {
				return ContainerTemplateSpecWriteOutput{}, fmt.Errorf("%s: container template spec writer is not available in this runtime", UpdateContainerTemplateSpecToolName)
			}

			update := runtimecontext.ContainerTemplateSpecUpdateInput{
				SpecID:               specID,
				Name:                 trimStringPointer(in.Name),
				Description:          trimStringPointer(in.Description),
				Command:              trimStringPointer(in.Command),
				CPU:                  in.CPU,
				Memory:               in.Memory,
				Port:                 in.Port,
				AppType:              trimStringPointer(in.AppType),
				Env:                  in.Env,
				Mounts:               in.Mounts,
				SchedulingConstraint: in.SchedulingConstraint,
				Labels:               in.Labels,
				ChangeUID:            in.ChangeUID,
			}

			record, updateErr := writer.UpdateContainerTemplateSpec(ctx, update)
			if updateErr != nil {
				return ContainerTemplateSpecWriteOutput{
					ContainerTemplateSpecRecord: runtimecontext.ContainerTemplateSpecRecord{SpecID: specID},
					Error:                       updateErr.Error(),
				}, nil
			}
			return ContainerTemplateSpecWriteOutput{ContainerTemplateSpecRecord: record}, nil
		},
	).WithPermission("write", "", "更新容器模板共享运行配置")
}

// hasContainerTemplateSpecUpdate 判断更新入参是否至少给出一个字段。
func hasContainerTemplateSpecUpdate(in UpdateContainerTemplateSpecInput) bool {
	return in.Name != nil ||
		in.Description != nil ||
		in.Command != nil ||
		in.CPU != nil ||
		in.Memory != nil ||
		in.Port != nil ||
		in.AppType != nil ||
		in.Env != nil ||
		in.Mounts != nil ||
		in.SchedulingConstraint != nil ||
		in.Labels != nil ||
		in.ChangeUID != nil
}

// trimStringPointer 复制并裁剪字符串指针；nil 原样返回。
func trimStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}
