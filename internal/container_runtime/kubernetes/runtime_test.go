package kubernetes

import (
	"context"
	"testing"
	"time"

	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type testRuntimeEventHandler struct {
	events []containerruntime.RuntimeEvent
}

type testRuntimeMonitor struct {
	called        int
	lastRuntimeID string
	err           error
}

func (m *testRuntimeMonitor) Monitor(_ context.Context, runtimeID string) error {
	m.called++
	m.lastRuntimeID = runtimeID
	return m.err
}

func (h *testRuntimeEventHandler) OnEvent(evt containerruntime.RuntimeEvent) {
	h.events = append(h.events, evt)
}

func TestIsPodReady_NilPod(t *testing.T) {
	if isPodReady(nil) {
		t.Fatalf("expected nil pod to be not ready")
	}
}

func TestIsPodReady_ReadyConditionTrue(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	if !isPodReady(pod) {
		t.Fatalf("expected pod to be ready")
	}
}

func TestIsPodReady_ReadyConditionFalse(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionFalse,
			}},
		},
	}

	if isPodReady(pod) {
		t.Fatalf("expected pod with Ready=False to be not ready")
	}
}

func TestIsPodReady_NodeAssignedButNotReady(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{NodeName: "worker-1"},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodScheduled,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	if isPodReady(pod) {
		t.Fatalf("expected pod with node assignment but without Ready=True to be not ready")
	}
}

func TestJobHasStarted_WithActivePod(t *testing.T) {
	job := &batchv1.Job{Status: batchv1.JobStatus{Active: 1}}
	if !jobHasStarted(job) {
		t.Fatalf("expected job with active pod to be started")
	}
}

func TestJobHasStarted_WithStartTime(t *testing.T) {
	now := metav1.NewTime(time.Now())
	job := &batchv1.Job{Status: batchv1.JobStatus{StartTime: &now}}
	if !jobHasStarted(job) {
		t.Fatalf("expected job with start time to be started")
	}
}

func TestJobFailureMessage_UsesConditionMessage(t *testing.T) {
	job := &batchv1.Job{
		Status: batchv1.JobStatus{
			Failed: 2,
			Conditions: []batchv1.JobCondition{{
				Type:    batchv1.JobFailed,
				Message: "image pull backoff",
			}},
		},
	}

	if got := jobFailureMessage(job); got != "image pull backoff" {
		t.Fatalf("unexpected failure message: %s", got)
	}
}

func TestDeploymentHasStarted(t *testing.T) {
	dep := &appsv1.Deployment{Status: appsv1.DeploymentStatus{ReadyReplicas: 1}}
	if !deploymentHasStarted(dep) {
		t.Fatalf("expected deployment with ready replicas to be started")
	}
}

func TestDeploymentHasExited_WhenScaledToZeroAndNoReplicas(t *testing.T) {
	zero := int32(0)
	dep := &appsv1.Deployment{
		Spec:   appsv1.DeploymentSpec{Replicas: &zero},
		Status: appsv1.DeploymentStatus{Replicas: 0},
	}
	if !deploymentHasExited(dep) {
		t.Fatalf("expected deployment scaled to zero with no replicas to be exited")
	}
}

func TestDeploymentFailureMessage_ProgressDeadlineExceeded(t *testing.T) {
	dep := &appsv1.Deployment{
		Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{{
			Type:    appsv1.DeploymentProgressing,
			Status:  corev1.ConditionFalse,
			Reason:  "ProgressDeadlineExceeded",
			Message: "deployment exceeded deadline",
		}}},
	}

	msg, failed := deploymentFailureMessage(dep)
	if !failed {
		t.Fatalf("expected deployment to be failed")
	}
	if msg != "deployment exceeded deadline" {
		t.Fatalf("unexpected failure message: %s", msg)
	}
}

func TestDeleteJob_EmitsContainerDeletedWhenNotMonitored(t *testing.T) {
	handler := &testRuntimeEventHandler{}
	k := &KubernetesRuntime{
		name:      "k8s",
		namespace: "default",
		clientset: fake.NewSimpleClientset(&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "demo-job", Namespace: "default"},
		}),
	}
	k.SetEventHandler(handler)

	runtimeID := k.runtimeID("default", workloadKindJob, "demo-job")
	for containerruntime.IsRuntimeMonitoring(runtimeID) {
		containerruntime.UnmarkRuntimeMonitoring(runtimeID)
	}

	if err := k.Delete(context.Background(), runtimeID); err != nil {
		t.Fatalf("delete job failed: %v", err)
	}

	if len(handler.events) != 1 {
		t.Fatalf("expected exactly one runtime event, got %d", len(handler.events))
	}
	if handler.events[0].Type != "ContainerDeleted" {
		t.Fatalf("expected ContainerDeleted event, got %s", handler.events[0].Type)
	}
	if handler.events[0].RuntimeID != runtimeID {
		t.Fatalf("unexpected runtime id in event, got %s", handler.events[0].RuntimeID)
	}

	_, err := k.clientset.BatchV1().Jobs("default").Get(context.Background(), "demo-job", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected job to be deleted, err=%v", err)
	}
}

func TestStopJob_EmitsContainerExitedWhenNotMonitored(t *testing.T) {
	handler := &testRuntimeEventHandler{}
	k := &KubernetesRuntime{
		name:      "k8s",
		namespace: "default",
		clientset: fake.NewSimpleClientset(&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "stop-job", Namespace: "default"},
			Spec:       batchv1.JobSpec{Suspend: boolPtr(false)},
		}),
	}
	k.SetEventHandler(handler)

	runtimeID := k.runtimeID("default", workloadKindJob, "stop-job")
	for containerruntime.IsRuntimeMonitoring(runtimeID) {
		containerruntime.UnmarkRuntimeMonitoring(runtimeID)
	}

	if err := k.Stop(context.Background(), runtimeID); err != nil {
		t.Fatalf("stop job failed: %v", err)
	}

	if len(handler.events) != 1 {
		t.Fatalf("expected exactly one runtime event, got %d", len(handler.events))
	}
	if handler.events[0].Type != "ContainerExited" {
		t.Fatalf("expected ContainerExited event, got %s", handler.events[0].Type)
	}
	if handler.events[0].Message != "0" {
		t.Fatalf("expected exit code message 0, got %s", handler.events[0].Message)
	}

	job, err := k.clientset.BatchV1().Jobs("default").Get(context.Background(), "stop-job", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected job to remain after stop(suspend), err=%v", err)
	}
	if job.Spec.Suspend == nil || !*job.Spec.Suspend {
		t.Fatalf("expected job to be suspended after stop")
	}
}

func TestResumeJob_UnsuspendsAndMonitors(t *testing.T) {
	handler := &testRuntimeEventHandler{}
	monitor := &testRuntimeMonitor{}
	k := &KubernetesRuntime{
		name:      "k8s",
		namespace: "default",
		clientset: fake.NewSimpleClientset(&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "resume-job", Namespace: "default"},
			Spec:       batchv1.JobSpec{Suspend: boolPtr(true)},
		}),
		monitor: monitor,
	}
	k.SetEventHandler(handler)

	runtimeID := k.runtimeID("default", workloadKindJob, "resume-job")
	for containerruntime.IsRuntimeMonitoring(runtimeID) {
		containerruntime.UnmarkRuntimeMonitoring(runtimeID)
	}

	if err := k.Resume(context.Background(), runtimeID); err != nil {
		t.Fatalf("resume job failed: %v", err)
	}

	if monitor.called != 1 {
		t.Fatalf("expected monitor to be called once, got %d", monitor.called)
	}
	if monitor.lastRuntimeID != runtimeID {
		t.Fatalf("unexpected runtime id in monitor call, got %s", monitor.lastRuntimeID)
	}

	job, err := k.clientset.BatchV1().Jobs("default").Get(context.Background(), "resume-job", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get resumed job failed: %v", err)
	}
	if job.Spec.Suspend == nil || *job.Spec.Suspend {
		t.Fatalf("expected job to be unsuspended after resume")
	}
}
