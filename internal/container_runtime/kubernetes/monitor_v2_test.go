package kubernetes

import (
	"testing"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// newJobMonitorFixture builds the smallest state a job lifecycle monitor needs: an
// event handler, a fake clientset holding the job, and one registered subscription
// for that job - exactly what Monitor() creates before the informer starts
// delivering deltas.
func newJobMonitorFixture(t *testing.T, job *batchv1.Job) (*kubernetesMonitorV2, *monitorSubscription, *testRuntimeEventHandler) {
	t.Helper()

	handler := &testRuntimeEventHandler{}
	k := &KubernetesRuntime{
		name:      "k8s",
		namespace: "default",
		clientset: fake.NewSimpleClientset(job),
	}
	k.SetEventHandler(handler)
	m := newKubernetesMonitorV2(k)

	runtimeID := k.runtimeID("default", workloadKindJob, job.Name)
	for containerruntime.IsRuntimeMonitoring(runtimeID) {
		containerruntime.UnmarkRuntimeMonitoring(runtimeID)
	}
	if !containerruntime.MarkIfNotMonitoring(runtimeID) {
		t.Fatalf("expected runtime to be newly marked")
	}

	sub := &monitorSubscription{
		runtimeID: runtimeID,
		namespace: job.Namespace,
		name:      job.Name,
		kind:      workloadKindJob,
		state:     &monitorState{},
	}
	m.addSubscription(sub)
	return m, sub, handler
}

// pendingJob is a job exactly as createJob leaves it: suspended and never started.
//
// This is the state a DAG node container is in while the lifecycle monitor is being
// registered (Start unsuspends the job and only then calls Monitor), so it is the
// state an early informer delta or snapshot probe can still observe.
func pendingJob(name string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       batchv1.JobSpec{Suspend: boolPtr(true)},
	}
}

// startedJob is a job the cluster has actually run: it has a start time and an
// active pod, and its suspension flag reflects whether it was asked to stop.
func startedJob(name string, suspend bool) *batchv1.Job {
	started := metav1.Now()
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       batchv1.JobSpec{Suspend: boolPtr(suspend)},
		Status:     batchv1.JobStatus{StartTime: &started, Active: 1},
	}
}

// A job that was created suspended and has not started yet must NOT be reported as
// exited.
//
// Regression test for the intermittent "output validation failed: missing output"
// DAG node failures: the job behind a freshly dispatched node is created suspended,
// and an informer delta carrying that pre-start object was reported as
// ContainerExited / exit 0. NodeCompletionCoordinator then believed the container had
// finished successfully milliseconds after it was created, found no outputs.json (the
// dispatcher cleans the output dir before executing) and failed the node while the
// real container was still running.
func TestHandleJobEvent_SuspendedJobThatNeverStartedIsNotReportedAsExited(t *testing.T) {
	job := pendingJob("suspend-job")
	m, sub, handler := newJobMonitorFixture(t, job)

	m.handleJobEvent(sub, job)

	if len(handler.events) != 0 {
		t.Fatalf("a job created suspended and never started must not be reported as exited, got %+v", handler.events)
	}
	if !containerruntime.IsRuntimeMonitoring(sub.runtimeID) {
		t.Fatalf("expected runtime monitoring to stay active so the real exit can still be reported")
	}
	if _, found := m.getSubscription(workloadKindJob, "default", job.Name); !found {
		t.Fatalf("expected subscription to stay registered")
	}
}

// The snapshot probe runs right after the subscription is registered and must be just
// as careful: a job still suspended because it never started is not an exit either.
func TestCheckJobSnapshot_SuspendedJobThatNeverStartedIsNotReportedAsExited(t *testing.T) {
	job := pendingJob("suspend-snapshot-job")
	m, sub, handler := newJobMonitorFixture(t, job)

	if done := m.checkJobSnapshot(sub); done {
		t.Fatalf("expected snapshot check to report the job as not finished")
	}

	if len(handler.events) != 0 {
		t.Fatalf("a job created suspended and never started must not be reported as exited, got %+v", handler.events)
	}
	if _, found := m.getSubscription(workloadKindJob, "default", job.Name); !found {
		t.Fatalf("expected subscription to stay registered")
	}
}

// Suspending a job that has actually been running is a real stop: the runtime suspends
// the job on Stop, and the monitor must still report it as exited.
func TestHandleJobEvent_EmitsExitedWhenStartedJobIsSuspended(t *testing.T) {
	job := startedJob("stop-running-job", true)
	m, sub, handler := newJobMonitorFixture(t, job)

	m.handleJobEvent(sub, job)

	if len(handler.events) != 2 {
		t.Fatalf("expected ContainerStarted and ContainerExited, got %+v", handler.events)
	}
	if handler.events[0].Type != "ContainerStarted" {
		t.Fatalf("expected first event to be ContainerStarted, got %s", handler.events[0].Type)
	}
	if handler.events[1].Type != "ContainerExited" {
		t.Fatalf("expected second event to be ContainerExited, got %s", handler.events[1].Type)
	}
	if handler.events[1].Message != "0" {
		t.Fatalf("expected exit code message 0, got %s", handler.events[1].Message)
	}
	if containerruntime.IsRuntimeMonitoring(sub.runtimeID) {
		t.Fatalf("expected runtime monitoring to be cleared")
	}
	if _, found := m.getSubscription(workloadKindJob, "default", job.Name); found {
		t.Fatalf("expected subscription to be removed")
	}
}

// A job that finished on its own is still reported as exited: the suspension guard
// must not swallow genuine completions.
func TestHandleJobEvent_EmitsExitedWhenJobSucceeded(t *testing.T) {
	job := startedJob("succeeded-job", false)
	job.Status.Active = 0
	job.Status.Succeeded = 1
	m, sub, handler := newJobMonitorFixture(t, job)

	m.handleJobEvent(sub, job)

	if len(handler.events) != 2 {
		t.Fatalf("expected ContainerStarted and ContainerExited, got %+v", handler.events)
	}
	if handler.events[1].Type != "ContainerExited" {
		t.Fatalf("expected ContainerExited event, got %s", handler.events[1].Type)
	}
	if _, found := m.getSubscription(workloadKindJob, "default", job.Name); found {
		t.Fatalf("expected subscription to be removed")
	}
}
