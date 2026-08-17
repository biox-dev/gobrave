package manager

import (
	"context"
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/gobravedev/gobrave/internal/types"
)

var runtimeVariablePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

type ContainerRuntimeResolveInput struct {
	Spec      *types.ContainerSpec
	Variables map[string]string
}

type ContainerRuntimeResolver interface {
	Resolve(ctx context.Context, in *ContainerRuntimeResolveInput) (*types.ContainerSpec, error)
}

type defaultContainerRuntimeResolver struct{}

func NewDefaultContainerRuntimeResolver() ContainerRuntimeResolver {
	return &defaultContainerRuntimeResolver{}
}

func (r *defaultContainerRuntimeResolver) Resolve(_ context.Context, in *ContainerRuntimeResolveInput) (*types.ContainerSpec, error) {
	if in == nil || in.Spec == nil {
		return nil, errors.New("container runtime resolve input/spec is required")
	}

	resolved := cloneContainerSpec(in.Spec)
	resolved.Image = resolveTemplateVariables(strings.TrimSpace(resolved.Image), in.Variables)
	resolved.WorkDir = resolveTemplateVariables(strings.TrimSpace(resolved.WorkDir), in.Variables)
	resolved.Entrypoint = resolveStringSlice(resolved.Entrypoint, in.Variables)
	resolved.Command = resolveStringSlice(resolved.Command, in.Variables)
	resolved.SchedulingConstraint = resolveSchedulingConstraint(resolved.SchedulingConstraint, in.Variables)

	resolvedEnv := make(map[string]string, len(resolved.Env))
	envKeys := make([]string, 0, len(resolved.Env))
	for k := range resolved.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, rawKey := range envKeys {
		resolvedKey := resolveTemplateVariables(strings.TrimSpace(rawKey), in.Variables)
		if resolvedKey == "" {
			continue
		}
		resolvedEnv[resolvedKey] = resolveTemplateVariables(resolved.Env[rawKey], in.Variables)
	}
	resolved.Env = resolvedEnv

	lookup := map[string]string{}
	for k, v := range in.Variables {
		lookup[k] = v
	}
	for k, v := range resolved.Env {
		lookup[k] = v
	}

	resolved.Volumes = resolveVolumes(resolved.Volumes, lookup)
	return resolved, nil
}

func cloneContainerSpec(spec *types.ContainerSpec) *types.ContainerSpec {
	cloned := &types.ContainerSpec{}
	if spec == nil {
		return cloned
	}

	*cloned = *spec
	if spec.Command != nil {
		cloned.Command = append([]string(nil), spec.Command...)
	}
	if spec.Entrypoint != nil {
		cloned.Entrypoint = append([]string(nil), spec.Entrypoint...)
	}
	if spec.Env != nil {
		cloned.Env = make(map[string]string, len(spec.Env))
		for k, v := range spec.Env {
			cloned.Env[k] = v
		}
	}
	if spec.Labels != nil {
		cloned.Labels = make(map[string]string, len(spec.Labels))
		for k, v := range spec.Labels {
			cloned.Labels[k] = v
		}
	}
	if spec.Volumes != nil {
		cloned.Volumes = append([]types.ContainerVolume(nil), spec.Volumes...)
	}
	if spec.SupplementalGroups != nil {
		cloned.SupplementalGroups = append([]int64(nil), spec.SupplementalGroups...)
	}
	if spec.SchedulingConstraint != nil {
		constraints := make([]types.ContainerSchedulingConstraint, 0, len(spec.SchedulingConstraint.Constraints))
		for _, item := range spec.SchedulingConstraint.Constraints {
			copied := types.ContainerSchedulingConstraint{
				Type:     item.Type,
				Key:      item.Key,
				Operator: item.Operator,
			}
			if item.Values != nil {
				copied.Values = append([]string(nil), item.Values...)
			}
			constraints = append(constraints, copied)
		}
		cloned.SchedulingConstraint = &types.ContainerSchedulingSelector{Constraints: constraints}
	}

	return cloned
}

func resolveVolumes(volumes []types.ContainerVolume, vars map[string]string) []types.ContainerVolume {
	if len(volumes) == 0 {
		return nil
	}

	resolved := make([]types.ContainerVolume, 0, len(volumes))
	for _, vol := range volumes {
		source := resolveTemplateVariables(strings.TrimSpace(vol.Source), vars)
		target := resolveTemplateVariables(strings.TrimSpace(vol.Target), vars)
		mode := resolveTemplateVariables(strings.TrimSpace(vol.Mode), vars)
		if source == "" || target == "" {
			continue
		}
		resolved = append(resolved, types.ContainerVolume{Source: source, Target: target, Mode: mode})
	}
	return resolved
}

func resolveStringSlice(values []string, vars map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, item := range values {
		value := resolveTemplateVariables(strings.TrimSpace(item), vars)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func resolveSchedulingConstraint(selector *types.ContainerSchedulingSelector, vars map[string]string) *types.ContainerSchedulingSelector {
	if selector == nil || len(selector.Constraints) == 0 {
		return nil
	}

	constraints := make([]types.ContainerSchedulingConstraint, 0, len(selector.Constraints))
	for _, item := range selector.Constraints {
		constraintType := resolveTemplateVariables(strings.TrimSpace(item.Type), vars)
		key := resolveTemplateVariables(strings.TrimSpace(item.Key), vars)
		operator := resolveTemplateVariables(strings.TrimSpace(item.Operator), vars)
		if constraintType == "" || key == "" || operator == "" {
			continue
		}

		resolvedValues := resolveStringSlice(item.Values, vars)
		constraints = append(constraints, types.ContainerSchedulingConstraint{
			Type:     constraintType,
			Key:      key,
			Operator: operator,
			Values:   resolvedValues,
		})
	}

	if len(constraints) == 0 {
		return nil
	}
	return &types.ContainerSchedulingSelector{Constraints: constraints}
}

func resolveTemplateVariables(raw string, vars map[string]string) string {
	if raw == "" {
		return ""
	}

	return runtimeVariablePattern.ReplaceAllStringFunc(raw, func(match string) string {
		parts := runtimeVariablePattern.FindStringSubmatch(match)
		key := parts[1]
		if key == "" {
			key = parts[2]
		}
		if key == "" {
			return match
		}
		if v, ok := vars[key]; ok {
			return v
		}
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		return match
	})
}
