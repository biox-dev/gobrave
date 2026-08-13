package kubernetes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
	"github.com/gobravedev/gobrave/internal/types"
)

const (
	defaultNamespace       = "default"
	defaultServiceSuffix   = "-svc"
	workloadKindDeployment = "deployment"
	workloadKindJob        = "job"
)

type KubernetesRuntimeConfig struct {
	RuntimeName string
	Namespace   string
	Kubeconfig  string
	InCluster   bool
}

type KubernetesRuntimeMonitor interface {
	Monitor(ctx context.Context, runtimeID string) error
}

type KubernetesRuntimeOption func(*KubernetesRuntime)

func WithKubernetesRuntimeMonitor(monitor KubernetesRuntimeMonitor) KubernetesRuntimeOption {
	return func(runtime *KubernetesRuntime) {
		if runtime == nil || monitor == nil {
			return
		}
		runtime.monitor = monitor
	}
}

type KubernetesRuntime struct {
	name      string
	namespace string
	clientset kubernetes.Interface
	monitor   KubernetesRuntimeMonitor

	handler containerruntime.RuntimeEventHandler
}

func NewKubernetesRuntime(cfg KubernetesRuntimeConfig) (*KubernetesRuntime, error) {
	return NewKubernetesRuntimeWithOptions(cfg)
}

func NewKubernetesRuntimeWithOptions(cfg KubernetesRuntimeConfig, opts ...KubernetesRuntimeOption) (*KubernetesRuntime, error) {
	runtimeName := strings.TrimSpace(strings.ToLower(cfg.RuntimeName))
	if runtimeName == "" {
		runtimeName = "k8s"
	}
	if runtimeName != "k8s" && runtimeName != "k3s" {
		return nil, fmt.Errorf("unsupported kubernetes runtime name: %s", cfg.RuntimeName)
	}

	namespace := strings.TrimSpace(cfg.Namespace)
	if namespace == "" {
		namespace = defaultNamespace
	}

	restCfg, err := buildRESTConfig(cfg)
	if err != nil {
		return nil, err
	}

	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("init kubernetes client: %w", err)
	}

	runtime := &KubernetesRuntime{name: runtimeName, namespace: namespace, clientset: cs}
	for _, opt := range opts {
		if opt != nil {
			opt(runtime)
		}
	}
	if runtime.monitor == nil {
		runtime.monitor = newKubernetesMonitorV2(runtime)
	}

	return runtime, nil
}

func (k *KubernetesRuntime) Name() string {
	return k.name
}

// EnsureImage satisfies containerruntime.RuntimeImageManager.
// For Kubernetes runtimes, image pull is performed by kubelet during pod scheduling.
// This method validates input and enforces pull-policy constraints that can be checked early.
func (k *KubernetesRuntime) EnsureImage(_ context.Context, imageName string, pullPolicy string) error {
	imageName = strings.TrimSpace(imageName)
	if imageName == "" {
		return errors.New("image is required")
	}

	switch normalizePullPolicy(pullPolicy) {
	case types.PullPolicyAlways, types.PullPolicyIfNotPresent:
		return nil
	case types.PullPolicyNever:
		return fmt.Errorf("pull policy %s is not supported for kubernetes runtime preflight; kubelet-local image cache cannot be verified centrally", types.PullPolicyNever)
	default:
		return fmt.Errorf("unsupported pull policy: %s", pullPolicy)
	}
}

func (k *KubernetesRuntime) Create(ctx context.Context, spec *types.ContainerSpec) (string, error) {
	if spec == nil {
		return "", errors.New("container spec is nil")
	}
	if strings.TrimSpace(spec.Image) == "" {
		return "", errors.New("container image is required")
	}

	ns := k.resolveNamespace(spec)
	workloadName := sanitizeName(spec.RuntimeName)
	if workloadName == "" {
		workloadName = fmt.Sprintf("%s-%d", k.name, time.Now().UnixNano())
	}

	kind := strings.TrimSpace(strings.ToLower(spec.WorkloadKind))
	if kind == "" {
		kind = workloadKindDeployment
	}

	switch kind {
	case workloadKindDeployment:
		if err := k.createDeployment(ctx, ns, workloadName, spec); err != nil {
			return "", err
		}
		if spec.ExposeService && spec.ExposedPort > 0 {
			if err := k.createService(ctx, ns, workloadName, spec.ExposedPort); err != nil {
				_ = k.clientset.AppsV1().Deployments(ns).Delete(context.Background(), workloadName, metav1.DeleteOptions{})
				return "", err
			}
		}
		return k.runtimeID(ns, workloadKindDeployment, workloadName), nil
	case workloadKindJob:
		if err := k.createJob(ctx, ns, workloadName, spec); err != nil {
			return "", err
		}
		return k.runtimeID(ns, workloadKindJob, workloadName), nil
	default:
		return "", fmt.Errorf("unsupported workload kind: %s", spec.WorkloadKind)
	}
}

func (k *KubernetesRuntime) Start(ctx context.Context, runtimeID string) error {
	meta, err := k.parseRuntimeID(runtimeID)
	if err != nil {
		return err
	}

	switch meta.Kind {
	case workloadKindDeployment:
		if err := k.scaleDeployment(ctx, meta.Namespace, meta.Name, 1); err != nil {
			return err
		}
		return k.Monitor(ctx, runtimeID)
	case workloadKindJob:
		_, err := k.clientset.BatchV1().Jobs(meta.Namespace).Get(ctx, meta.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("job workload is not restartable after delete: %s", runtimeID)
			}
			return fmt.Errorf("get job %s: %w", meta.Name, err)
		}
		return k.Monitor(ctx, runtimeID)
	default:
		return fmt.Errorf("unsupported workload kind: %s", meta.Kind)
	}
}

func (k *KubernetesRuntime) Stop(ctx context.Context, runtimeID string) error {
	meta, err := k.parseRuntimeID(runtimeID)
	if err != nil {
		return err
	}

	switch meta.Kind {
	case workloadKindDeployment:
		if err := k.scaleDeployment(ctx, meta.Namespace, meta.Name, 0); err != nil {
			if apierrors.IsNotFound(err) {
				k.emitEvent("ContainerExited", runtimeID, "deployment not found")
				return nil
			}
			return err
		}
		return nil
	case workloadKindJob:
		policy := metav1.DeletePropagationForeground
		err := k.clientset.BatchV1().Jobs(meta.Namespace).Delete(ctx, meta.Name, metav1.DeleteOptions{PropagationPolicy: &policy})
		if err != nil {
			if apierrors.IsNotFound(err) {
				k.emitEvent("ContainerExited", runtimeID, "job not found")
				return nil
			}
			return fmt.Errorf("delete job %s: %w", meta.Name, err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported workload kind: %s", meta.Kind)
	}
}

func (k *KubernetesRuntime) Pause(ctx context.Context, runtimeID string) error {
	return k.Stop(ctx, runtimeID)
}

func (k *KubernetesRuntime) Resume(ctx context.Context, runtimeID string) error {
	return k.Start(ctx, runtimeID)
}

func (k *KubernetesRuntime) Delete(ctx context.Context, runtimeID string) error {
	meta, err := k.parseRuntimeID(runtimeID)
	if err != nil {
		return err
	}

	switch meta.Kind {
	case workloadKindDeployment:
		svcName := serviceNameForWorkload(meta.Name)
		if err := k.clientset.CoreV1().Services(meta.Namespace).Delete(ctx, svcName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete service %s: %w", svcName, err)
		}
		if err := k.clientset.AppsV1().Deployments(meta.Namespace).Delete(ctx, meta.Name, metav1.DeleteOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				k.emitEvent("ContainerDeleted", runtimeID, "deployment not found")
				return nil
			}
			return fmt.Errorf("delete deployment %s: %w", meta.Name, err)
		}
		if !containerruntime.IsRuntimeMonitoring(runtimeID) {
			k.emitEvent("ContainerDeleted", runtimeID, "deployment deleted")
		}
		return nil
	case workloadKindJob:
		policy := metav1.DeletePropagationForeground
		if err := k.clientset.BatchV1().Jobs(meta.Namespace).Delete(ctx, meta.Name, metav1.DeleteOptions{PropagationPolicy: &policy}); err != nil {
			if apierrors.IsNotFound(err) {
				k.emitEvent("ContainerDeleted", runtimeID, "job not found")
				return nil
			}
			return fmt.Errorf("delete job %s: %w", meta.Name, err)
		}
		if !containerruntime.IsRuntimeMonitoring(runtimeID) {
			k.emitEvent("ContainerDeleted", runtimeID, "job deleted")
		}
		return nil
	default:
		return fmt.Errorf("unsupported workload kind: %s", meta.Kind)
	}
}

func (k *KubernetesRuntime) Logs(ctx context.Context, runtimeID string, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}

	meta, err := k.parseRuntimeID(runtimeID)
	if err != nil {
		return "", err
	}

	podName, err := k.resolveLatestPodName(ctx, meta.Namespace, map[string]string{"gobrave-workload": meta.Name})
	if err != nil {
		return "", err
	}

	count := int64(tail)
	r := k.clientset.CoreV1().Pods(meta.Namespace).GetLogs(podName, &corev1.PodLogOptions{TailLines: &count})
	stream, err := r.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("read pod logs %s: %w", podName, err)
	}
	defer stream.Close()

	var out bytes.Buffer
	if _, err := io.Copy(&out, stream); err != nil {
		return "", fmt.Errorf("copy pod logs %s: %w", podName, err)
	}
	return out.String(), nil
}

func (k *KubernetesRuntime) SetEventHandler(handler containerruntime.RuntimeEventHandler) {
	k.handler = handler
}

func (k *KubernetesRuntime) Exec(_ context.Context, _ string, _ []string) (string, error) {
	return "", errors.New("kubernetes runtime exec is not implemented")
}

func (k *KubernetesRuntime) Inspect(ctx context.Context, runtimeID string) (*containerruntime.RuntimeInspection, error) {
	meta, err := k.parseRuntimeID(runtimeID)
	if err != nil {
		return nil, err
	}

	switch meta.Kind {
	case workloadKindDeployment:
		host := serviceNameForWorkload(meta.Name) + "." + meta.Namespace + ".svc.cluster.local"
		inspection := &containerruntime.RuntimeInspection{IPAddress: host}
		podName, err := k.resolveLatestPodName(ctx, meta.Namespace, map[string]string{"gobrave-workload": meta.Name})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return inspection, nil
			}
			return nil, err
		}
		pod, err := k.clientset.CoreV1().Pods(meta.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return inspection, nil
			}
			return nil, fmt.Errorf("inspect pod %s: %w", podName, err)
		}
		inspection.NodeName = strings.TrimSpace(pod.Spec.NodeName)
		return inspection, nil
	case workloadKindJob:
		podName, err := k.resolveLatestPodName(ctx, meta.Namespace, map[string]string{"gobrave-workload": meta.Name})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return &containerruntime.RuntimeInspection{}, nil
			}
			return nil, err
		}
		pod, err := k.clientset.CoreV1().Pods(meta.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("inspect pod %s: %w", podName, err)
		}
		return &containerruntime.RuntimeInspection{
			IPAddress: strings.TrimSpace(pod.Status.PodIP),
			NodeName:  strings.TrimSpace(pod.Spec.NodeName),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported workload kind: %s", meta.Kind)
	}
}

// 监控 deployment 和 job 的生命周期（启动与退出）。
func (k *KubernetesRuntime) Monitor(ctx context.Context, runtimeID string) error {
	if k.monitor == nil {
		return newKubernetesMonitorV2(k).Monitor(ctx, runtimeID)
	}
	return k.monitor.Monitor(ctx, runtimeID)
}

func isPodReady(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (k *KubernetesRuntime) emitEvent(eventType string, runtimeID string, message string) {
	if k.handler == nil {
		return
	}
	k.handler.OnEvent(containerruntime.RuntimeEvent{Type: eventType, RuntimeID: runtimeID, Message: message})
}

func (k *KubernetesRuntime) resolveNamespace(spec *types.ContainerSpec) string {
	if spec != nil && strings.TrimSpace(spec.RuntimeNamespace) != "" {
		return strings.TrimSpace(spec.RuntimeNamespace)
	}
	if strings.TrimSpace(k.namespace) != "" {
		return strings.TrimSpace(k.namespace)
	}
	return defaultNamespace
}

func (k *KubernetesRuntime) createDeployment(ctx context.Context, namespace string, name string, spec *types.ContainerSpec) error {
	labels := mergeLabels(spec, map[string]string{
		"app":              name,
		"gobrave-workload": name,
	})

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       buildPodSpecForDeployment(spec),
			},
		},
	}

	_, err := k.clientset.AppsV1().Deployments(namespace).Create(ctx, deploy, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("deployment already exists: %s", name)
		}
		return fmt.Errorf("create deployment %s: %w", name, err)
	}
	return nil
}

func (k *KubernetesRuntime) createService(ctx context.Context, namespace string, workloadName string, port int) error {
	svcName := serviceNameForWorkload(workloadName)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: namespace, Labels: map[string]string{"app": workloadName, "gobrave-workload": workloadName}},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{"app": workloadName},
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       int32(port),
				TargetPort: intstr.FromInt(port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}

	_, err := k.clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create service %s: %w", svcName, err)
	}
	return nil
}

func (k *KubernetesRuntime) createJob(ctx context.Context, namespace string, name string, spec *types.ContainerSpec) error {
	labels := mergeLabels(spec, map[string]string{
		"app":              name,
		"gobrave-workload": name,
	})

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit: int32Ptr(0),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       buildPodSpecForJob(spec),
			},
		},
	}

	_, err := k.clientset.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("job already exists: %s", name)
		}
		return fmt.Errorf("create job %s: %w", name, err)
	}
	return nil
}

func buildPodSpecForDeployment(spec *types.ContainerSpec) corev1.PodSpec {
	podSpec := buildPodSpec(spec)
	podSpec.RestartPolicy = corev1.RestartPolicyAlways
	return podSpec
}

func buildPodSpecForJob(spec *types.ContainerSpec) corev1.PodSpec {
	podSpec := buildPodSpec(spec)
	podSpec.RestartPolicy = corev1.RestartPolicyNever
	return podSpec
}

func buildPodSpec(spec *types.ContainerSpec) corev1.PodSpec {
	env := make([]corev1.EnvVar, 0, len(spec.Env))
	envKeys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		env = append(env, corev1.EnvVar{Name: key, Value: spec.Env[key]})
	}

	resources := corev1.ResourceRequirements{}
	if spec.CPU > 0 {
		resources.Limits = corev1.ResourceList{}
		resources.Limits[corev1.ResourceCPU] = resource.MustParse(fmt.Sprintf("%gm", spec.CPU*1000))
	}
	if spec.Memory > 0 {
		if resources.Limits == nil {
			resources.Limits = corev1.ResourceList{}
		}
		resources.Limits[corev1.ResourceMemory] = resource.MustParse(strconv.FormatInt(spec.Memory, 10))
	}

	mounts, volumes := buildPodVolumes(spec.Volumes)

	container := corev1.Container{
		Name:            "main",
		Image:           spec.Image,
		Command:         append([]string(nil), spec.Entrypoint...),
		Args:            append([]string(nil), spec.Command...),
		Env:             env,
		WorkingDir:      strings.TrimSpace(spec.WorkDir),
		VolumeMounts:    mounts,
		Resources:       resources,
		ImagePullPolicy: corev1.PullIfNotPresent,
	}
	if strings.TrimSpace(spec.User) != "" {
		container.SecurityContext = &corev1.SecurityContext{RunAsUser: parseUserID(spec.User)}
	}
	if spec.ExposedPort > 0 {
		container.Ports = []corev1.ContainerPort{{ContainerPort: int32(spec.ExposedPort)}}
	}

	affinity := buildNodeAffinity(spec.SchedulingConstraint)

	return corev1.PodSpec{
		Containers: []corev1.Container{container},
		Volumes:    volumes,
		Affinity:   affinity,
	}
}

func buildNodeAffinity(selector *types.ContainerSchedulingSelector) *corev1.Affinity {
	if selector == nil || len(selector.Constraints) == 0 {
		return nil
	}

	requirements := make([]corev1.NodeSelectorRequirement, 0, len(selector.Constraints))
	for _, rawConstraint := range selector.Constraints {
		if !strings.EqualFold(strings.TrimSpace(rawConstraint.Type), "node") {
			continue
		}

		key := normalizeNodeConstraintKey(rawConstraint.Key)
		op, ok := toNodeSelectorOperator(rawConstraint.Operator)
		if !ok || key == "" {
			continue
		}

		values := normalizeConstraintValues(rawConstraint.Values)
		if nodeSelectorOperatorNeedsValues(op) && len(values) == 0 {
			continue
		}
		if !nodeSelectorOperatorNeedsValues(op) {
			values = nil
		}

		requirements = append(requirements, corev1.NodeSelectorRequirement{
			Key:      key,
			Operator: op,
			Values:   values,
		})
	}

	if len(requirements) == 0 {
		return nil
	}

	return &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: requirements,
				}},
			},
		},
	}
}

func normalizeNodeConstraintKey(raw string) string {
	key := strings.TrimSpace(raw)
	if key == "" {
		return ""
	}
	if strings.EqualFold(key, "hostname") {
		return corev1.LabelHostname
	}
	return key
}

func toNodeSelectorOperator(raw string) (corev1.NodeSelectorOperator, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "in":
		return corev1.NodeSelectorOpIn, true
	case "notin":
		return corev1.NodeSelectorOpNotIn, true
	case "exists":
		return corev1.NodeSelectorOpExists, true
	case "doesnotexist":
		return corev1.NodeSelectorOpDoesNotExist, true
	case "gt":
		return corev1.NodeSelectorOpGt, true
	case "lt":
		return corev1.NodeSelectorOpLt, true
	default:
		return "", false
	}
}

func nodeSelectorOperatorNeedsValues(op corev1.NodeSelectorOperator) bool {
	return op == corev1.NodeSelectorOpIn || op == corev1.NodeSelectorOpNotIn || op == corev1.NodeSelectorOpGt || op == corev1.NodeSelectorOpLt
}

func normalizeConstraintValues(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildPodVolumes(bindings []types.ContainerVolume) ([]corev1.VolumeMount, []corev1.Volume) {
	if len(bindings) == 0 {
		return nil, nil
	}
	mounts := make([]corev1.VolumeMount, 0, len(bindings))
	volumes := make([]corev1.Volume, 0, len(bindings))
	for i, bind := range bindings {
		source := strings.TrimSpace(bind.Source)
		target := strings.TrimSpace(bind.Target)
		if source == "" || target == "" {
			continue
		}
		volumeName := fmt.Sprintf("vol-%d", i)
		mounts = append(mounts, corev1.VolumeMount{Name: volumeName, MountPath: target})
		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: source},
			},
		})
	}
	return mounts, volumes
}

func mergeLabels(spec *types.ContainerSpec, defaults map[string]string) map[string]string {
	labels := map[string]string{}
	for key, value := range defaults {
		labels[key] = value
	}
	if spec == nil || len(spec.Labels) == 0 {
		return labels
	}
	for key, value := range spec.Labels {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		labels[key] = value
	}
	return labels
}

func (k *KubernetesRuntime) scaleDeployment(ctx context.Context, namespace, name string, replicas int32) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		scale, err := k.clientset.AppsV1().Deployments(namespace).GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if scale.Spec.Replicas == replicas {
			return nil
		}
		scale.Spec.Replicas = replicas
		_, err = k.clientset.AppsV1().Deployments(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("scale deployment %s: %w", name, err)
	}
	return nil
}

func (k *KubernetesRuntime) resolveLatestPodName(ctx context.Context, namespace string, labels map[string]string) (string, error) {
	selector := []string{}
	for key, value := range labels {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		selector = append(selector, key+"="+value)
	}
	list, err := k.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: strings.Join(selector, ",")})
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	if len(list.Items) == 0 {
		return "", apierrors.NewNotFound(corev1.Resource("pods"), strings.Join(selector, ","))
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].CreationTimestamp.Time.After(list.Items[j].CreationTimestamp.Time)
	})
	return list.Items[0].Name, nil
}

func (k *KubernetesRuntime) runtimeID(namespace, kind, name string) string {
	return k.name + "-" + namespace + "|" + kind + "|" + name
}

type runtimeMeta struct {
	Namespace string
	Kind      string
	Name      string
}

func (k *KubernetesRuntime) parseRuntimeID(runtimeID string) (*runtimeMeta, error) {
	id := strings.TrimSpace(runtimeID)
	prefix := k.name + "-"
	if !strings.HasPrefix(id, prefix) {
		return nil, fmt.Errorf("invalid runtime id prefix: %s", runtimeID)
	}
	raw := strings.TrimPrefix(id, prefix)
	parts := strings.Split(raw, "|")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid runtime id format: %s", runtimeID)
	}
	meta := &runtimeMeta{Namespace: strings.TrimSpace(parts[0]), Kind: strings.TrimSpace(parts[1]), Name: strings.TrimSpace(parts[2])}
	if meta.Namespace == "" || meta.Kind == "" || meta.Name == "" {
		return nil, fmt.Errorf("invalid runtime id format: %s", runtimeID)
	}
	return meta, nil
}

func serviceNameForWorkload(workloadName string) string {
	name := sanitizeName(workloadName)
	if name == "" {
		name = "workload"
	}
	if !strings.HasSuffix(name, defaultServiceSuffix) {
		name += defaultServiceSuffix
	}
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

func sanitizeName(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	builder := strings.Builder{}
	builder.Grow(len(raw))
	lastDash := false
	for _, r := range raw {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}
	name := strings.Trim(builder.String(), "-")
	if len(name) > 63 {
		name = strings.Trim(name[:63], "-")
	}
	if name == "" {
		return "workload"
	}
	return name
}

func int32Ptr(v int32) *int32 {
	return &v
}

func parseUserID(user string) *int64 {
	user = strings.TrimSpace(user)
	if user == "" {
		return nil
	}
	first := user
	if idx := strings.Index(first, ":"); idx >= 0 {
		first = first[:idx]
	}
	id, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	if err != nil {
		return nil
	}
	return &id
}

func buildRESTConfig(cfg KubernetesRuntimeConfig) (*rest.Config, error) {
	if cfg.InCluster {
		restCfg, err := rest.InClusterConfig()
		if err == nil {
			return restCfg, nil
		}
		return nil, fmt.Errorf("init in-cluster kubernetes config: %w", err)
	}

	kubeconfig := strings.TrimSpace(cfg.Kubeconfig)
	if kubeconfig != "" {
		restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig %s: %w", kubeconfig, err)
		}
		return restCfg, nil
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	restCfg, err := clientCfg.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load default kubeconfig: %w", err)
	}
	return restCfg, nil
}

func normalizePullPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case strings.ToLower(types.PullPolicyAlways):
		return types.PullPolicyAlways
	case strings.ToLower(types.PullPolicyNever):
		return types.PullPolicyNever
	case "", strings.ToLower(types.PullPolicyIfNotPresent):
		return types.PullPolicyIfNotPresent
	default:
		return strings.TrimSpace(policy)
	}
}
