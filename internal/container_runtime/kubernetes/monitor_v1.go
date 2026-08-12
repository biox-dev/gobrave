package kubernetes

import (
	"context"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
)

type kubernetesMonitorV1 struct {
	runtime *KubernetesRuntime
}

func newKubernetesMonitorV1(runtime *KubernetesRuntime) *kubernetesMonitorV1 {
	return &kubernetesMonitorV1{runtime: runtime}
}

// Monitor watches deployment/job lifecycle and emits runtime events.
func (m *kubernetesMonitorV1) Monitor(_ context.Context, runtimeID string) error {
	meta, err := m.runtime.parseRuntimeID(runtimeID)
	if err != nil {
		return err
	}

	if !containerruntime.MarkIfNotMonitoring(runtimeID) {
		return nil
	}

	switch meta.Kind {
	case workloadKindDeployment:
		go m.waitDeploymentLifecycle(runtimeID, meta.Namespace, meta.Name)
	case workloadKindJob:
		go m.waitJobLifecycle(runtimeID, meta.Namespace, meta.Name)
	}
	return nil
}

func (m *kubernetesMonitorV1) waitJobLifecycle(runtimeID string, namespace string, jobName string) {
	defer containerruntime.UnmarkRuntimeMonitoring(runtimeID)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	startedEmitted := false

	for {
		job, err := m.runtime.clientset.BatchV1().Jobs(namespace).Get(context.Background(), jobName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				m.runtime.emitEvent("ContainerDeleted", runtimeID, "job not found")
				return
			}
			m.runtime.emitEvent("ContainerFailed", runtimeID, err.Error())
			return
		}

		if !startedEmitted && jobHasStarted(job) {
			m.runtime.emitEvent("ContainerStarted", runtimeID, "")
			startedEmitted = true
		}

		if job.Status.Succeeded > 0 {
			m.runtime.emitEvent("ContainerExited", runtimeID, "0")
			return
		}
		if job.Status.Failed > 0 {
			msg := jobFailureMessage(job)
			m.runtime.emitEvent("ContainerFailed", runtimeID, msg)
			return
		}

		<-ticker.C
	}
}

func (m *kubernetesMonitorV1) waitDeploymentLifecycle(runtimeID string, namespace string, workloadName string) {
	defer containerruntime.UnmarkRuntimeMonitoring(runtimeID)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	startedEmitted := false

	for range ticker.C {
		dep, err := m.runtime.clientset.AppsV1().Deployments(namespace).Get(context.Background(), workloadName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				m.runtime.emitEvent("ContainerDeleted", runtimeID, "deployment not found")
				return
			}
			m.runtime.emitEvent("ContainerFailed", runtimeID, err.Error())
			return
		}

		if !startedEmitted && deploymentHasStarted(dep) {
			m.runtime.emitEvent("ContainerStarted", runtimeID, "")
			startedEmitted = true
		}

		if msg, failed := deploymentFailureMessage(dep); failed {
			m.runtime.emitEvent("ContainerFailed", runtimeID, msg)
			return
		}

		if deploymentHasExited(dep) {
			m.runtime.emitEvent("ContainerExited", runtimeID, "0")
			return
		}
	}
}

func jobHasStarted(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	if job.Status.Active > 0 {
		return true
	}
	return job.Status.StartTime != nil
}

func jobFailureMessage(job *batchv1.Job) string {
	if job == nil {
		return "job failed"
	}
	msg := strconv.Itoa(int(job.Status.Failed))
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && strings.TrimSpace(c.Message) != "" {
			return c.Message
		}
	}
	return msg
}

func deploymentHasStarted(dep *appsv1.Deployment) bool {
	if dep == nil {
		return false
	}
	return dep.Status.ReadyReplicas > 0
}

func deploymentHasExited(dep *appsv1.Deployment) bool {
	if dep == nil || dep.Spec.Replicas == nil {
		return false
	}
	return *dep.Spec.Replicas == 0 && dep.Status.Replicas == 0
}

func deploymentFailureMessage(dep *appsv1.Deployment) (string, bool) {
	if dep == nil {
		return "deployment is nil", true
	}
	for _, cond := range dep.Status.Conditions {
		if cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == corev1.ConditionTrue {
			msg := strings.TrimSpace(cond.Message)
			if msg == "" {
				msg = strings.TrimSpace(cond.Reason)
			}
			if msg == "" {
				msg = "deployment replica failure"
			}
			return msg, true
		}
		if cond.Type == appsv1.DeploymentProgressing && cond.Status == corev1.ConditionFalse && cond.Reason == "ProgressDeadlineExceeded" {
			msg := strings.TrimSpace(cond.Message)
			if msg == "" {
				msg = "deployment exceeded progress deadline"
			}
			return msg, true
		}
	}
	return "", false
}
