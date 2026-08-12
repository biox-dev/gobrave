package kubernetes

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
