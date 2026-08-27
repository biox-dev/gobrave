package kubernetes

import (
	"testing"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHandleJobEvent_EmitsExitedWhenSuspended(t *testing.T) {
	handler := &testRuntimeEventHandler{}
	k := &KubernetesRuntime{name: "k8s"}
	k.SetEventHandler(handler)
	m := newKubernetesMonitorV2(k)

	runtimeID := k.runtimeID("default", workloadKindJob, "suspend-job")
	for containerruntime.IsRuntimeMonitoring(runtimeID) {
		containerruntime.UnmarkRuntimeMonitoring(runtimeID)
	}
	if !containerruntime.MarkIfNotMonitoring(runtimeID) {
		t.Fatalf("expected runtime to be newly marked")
	}

	sub := &monitorSubscription{
		runtimeID: runtimeID,
		namespace: "default",
		name:      "suspend-job",
		kind:      workloadKindJob,
		state:     &monitorState{},
	}
	m.addSubscription(sub)

	m.handleJobEvent(sub, &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "suspend-job", Namespace: "default"},
		Spec:       batchv1.JobSpec{Suspend: boolPtr(true)},
	})

	if len(handler.events) != 1 {
		t.Fatalf("expected exactly one runtime event, got %d", len(handler.events))
	}
	if handler.events[0].Type != "ContainerExited" {
		t.Fatalf("expected ContainerExited event, got %s", handler.events[0].Type)
	}
	if handler.events[0].Message != "0" {
		t.Fatalf("expected exit code message 0, got %s", handler.events[0].Message)
	}
	if containerruntime.IsRuntimeMonitoring(runtimeID) {
		t.Fatalf("expected runtime monitoring to be cleared")
	}
	if _, found := m.getSubscription(workloadKindJob, "default", "suspend-job"); found {
		t.Fatalf("expected subscription to be removed")
	}
}
