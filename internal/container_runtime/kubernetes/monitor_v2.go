package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
)

type kubernetesMonitorV2 struct {
	runtime *KubernetesRuntime

	once      sync.Once
	startErr  error
	stopCh    chan struct{}
	depSynced cache.InformerSynced
	jobSynced cache.InformerSynced

	mu   sync.Mutex
	subs map[string]*monitorSubscription
}

func newKubernetesMonitorV2(runtime *KubernetesRuntime) *kubernetesMonitorV2 {
	return &kubernetesMonitorV2{
		runtime: runtime,
		subs:    map[string]*monitorSubscription{},
	}
}

// Monitor watches deployment/job lifecycle via informer events.
func (m *kubernetesMonitorV2) Monitor(_ context.Context, runtimeID string) error {
	meta, err := m.runtime.parseRuntimeID(runtimeID)
	if err != nil {
		return err
	}

	if !containerruntime.MarkIfNotMonitoring(runtimeID) {
		return nil
	}

	if err := m.ensureInformerStarted(); err != nil {
		containerruntime.UnmarkRuntimeMonitoring(runtimeID)
		return err
	}

	sub := &monitorSubscription{
		runtimeID: runtimeID,
		namespace: meta.Namespace,
		name:      meta.Name,
		kind:      meta.Kind,
		state:     &monitorState{},
	}

	switch meta.Kind {
	case workloadKindDeployment:
		m.addSubscription(sub)
		if done := m.checkDeploymentSnapshot(sub); done {
			m.completeAndCleanup(sub)
		}
	case workloadKindJob:
		m.addSubscription(sub)
		if done := m.checkJobSnapshot(sub); done {
			m.completeAndCleanup(sub)
		}
	default:
		containerruntime.UnmarkRuntimeMonitoring(runtimeID)
		return fmt.Errorf("unsupported workload kind: %s", meta.Kind)
	}
	return nil
}

type monitorSubscription struct {
	runtimeID string
	namespace string
	name      string
	kind      string
	state     *monitorState
}

type monitorState struct {
	mu      sync.Mutex
	started bool
	closed  bool
}

func (s *monitorState) emitStarted(emit func()) {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	emit()
}

func (s *monitorState) emitAndClose(emit func()) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.closed = true
	s.mu.Unlock()
	emit()
	return true
}

func (m *kubernetesMonitorV2) ensureInformerStarted() error {
	m.once.Do(func() {
		m.stopCh = make(chan struct{})
		factory := informers.NewSharedInformerFactoryWithOptions(m.runtime.clientset, 0)

		depInformer := factory.Apps().V1().Deployments().Informer()
		jobInformer := factory.Batch().V1().Jobs().Informer()

		if _, err := depInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    m.onDeploymentAddOrUpdate,
			UpdateFunc: func(_, newObj interface{}) { m.onDeploymentAddOrUpdate(newObj) },
			DeleteFunc: m.onDeploymentDelete,
		}); err != nil {
			m.startErr = fmt.Errorf("register deployment informer handler: %w", err)
			close(m.stopCh)
			return
		}

		if _, err := jobInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    m.onJobAddOrUpdate,
			UpdateFunc: func(_, newObj interface{}) { m.onJobAddOrUpdate(newObj) },
			DeleteFunc: m.onJobDelete,
		}); err != nil {
			m.startErr = fmt.Errorf("register job informer handler: %w", err)
			close(m.stopCh)
			return
		}

		m.depSynced = depInformer.HasSynced
		m.jobSynced = jobInformer.HasSynced

		factory.Start(m.stopCh)
		if !cache.WaitForCacheSync(m.stopCh, m.depSynced, m.jobSynced) {
			m.startErr = errors.New("sync informer cache failed")
			close(m.stopCh)
			return
		}
	})
	return m.startErr
}

func (m *kubernetesMonitorV2) onJobAddOrUpdate(obj interface{}) {
	job, ok := obj.(*batchv1.Job)
	if !ok {
		return
	}
	sub, found := m.getSubscription(workloadKindJob, job.Namespace, job.Name)
	if !found {
		return
	}
	m.handleJobEvent(sub, job)
}

func (m *kubernetesMonitorV2) onJobDelete(obj interface{}) {
	job, ok := obj.(*batchv1.Job)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		job, ok = tombstone.Obj.(*batchv1.Job)
		if !ok {
			return
		}
	}

	sub, found := m.getSubscription(workloadKindJob, job.Namespace, job.Name)
	if !found {
		return
	}
	if sub.state.emitAndClose(func() {
		m.runtime.emitEvent("ContainerDeleted", sub.runtimeID, "job not found")
	}) {
		m.completeAndCleanup(sub)
	}
}

func (m *kubernetesMonitorV2) onDeploymentAddOrUpdate(obj interface{}) {
	dep, ok := obj.(*appsv1.Deployment)
	if !ok {
		return
	}
	sub, found := m.getSubscription(workloadKindDeployment, dep.Namespace, dep.Name)
	if !found {
		return
	}
	m.handleDeploymentEvent(sub, dep)
}

func (m *kubernetesMonitorV2) onDeploymentDelete(obj interface{}) {
	dep, ok := obj.(*appsv1.Deployment)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		dep, ok = tombstone.Obj.(*appsv1.Deployment)
		if !ok {
			return
		}
	}

	sub, found := m.getSubscription(workloadKindDeployment, dep.Namespace, dep.Name)
	if !found {
		return
	}
	if sub.state.emitAndClose(func() {
		m.runtime.emitEvent("ContainerDeleted", sub.runtimeID, "deployment not found")
	}) {
		m.completeAndCleanup(sub)
	}
}

func (m *kubernetesMonitorV2) handleJobEvent(sub *monitorSubscription, job *batchv1.Job) {
	if jobHasStarted(job) {
		sub.state.emitStarted(func() {
			m.runtime.emitEvent("ContainerStarted", sub.runtimeID, "")
		})
	}
	if job.Status.Succeeded > 0 {
		if sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerExited", sub.runtimeID, "0")
		}) {
			m.completeAndCleanup(sub)
		}
		return
	}
	if job.Status.Failed > 0 {
		if sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerFailed", sub.runtimeID, jobFailureMessage(job))
		}) {
			m.completeAndCleanup(sub)
		}
	}
}

func (m *kubernetesMonitorV2) handleDeploymentEvent(sub *monitorSubscription, dep *appsv1.Deployment) {
	if deploymentHasStarted(dep) {
		sub.state.emitStarted(func() {
			m.runtime.emitEvent("ContainerStarted", sub.runtimeID, "")
		})
	}
	if msg, failed := deploymentFailureMessage(dep); failed {
		if sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerFailed", sub.runtimeID, msg)
		}) {
			m.completeAndCleanup(sub)
		}
		return
	}
	if deploymentHasExited(dep) {
		if sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerExited", sub.runtimeID, "0")
		}) {
			m.completeAndCleanup(sub)
		}
	}
}

// checkJobSnapshot reuses V1 status logic as a one-time pre-check before informer subscription settles.
func (m *kubernetesMonitorV2) checkJobSnapshot(sub *monitorSubscription) bool {
	job, err := m.runtime.clientset.BatchV1().Jobs(sub.namespace).Get(context.Background(), sub.name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return sub.state.emitAndClose(func() {
				m.runtime.emitEvent("ContainerDeleted", sub.runtimeID, "job not found")
			})
		}
		return sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerFailed", sub.runtimeID, err.Error())
		})
	}

	if jobHasStarted(job) {
		sub.state.emitStarted(func() {
			m.runtime.emitEvent("ContainerStarted", sub.runtimeID, "")
		})
	}
	if job.Status.Succeeded > 0 {
		return sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerExited", sub.runtimeID, "0")
		})
	}
	if job.Status.Failed > 0 {
		return sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerFailed", sub.runtimeID, jobFailureMessage(job))
		})
	}
	return false
}

// checkDeploymentSnapshot reuses V1 status logic as a one-time pre-check before informer subscription settles.
func (m *kubernetesMonitorV2) checkDeploymentSnapshot(sub *monitorSubscription) bool {
	dep, err := m.runtime.clientset.AppsV1().Deployments(sub.namespace).Get(context.Background(), sub.name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return sub.state.emitAndClose(func() {
				m.runtime.emitEvent("ContainerDeleted", sub.runtimeID, "deployment not found")
			})
		}
		return sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerFailed", sub.runtimeID, err.Error())
		})
	}

	if deploymentHasStarted(dep) {
		sub.state.emitStarted(func() {
			m.runtime.emitEvent("ContainerStarted", sub.runtimeID, "")
		})
	}
	if msg, failed := deploymentFailureMessage(dep); failed {
		return sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerFailed", sub.runtimeID, msg)
		})
	}
	if deploymentHasExited(dep) {
		return sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerExited", sub.runtimeID, "0")
		})
	}
	return false
}

func (m *kubernetesMonitorV2) addSubscription(sub *monitorSubscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs[m.subscriptionKey(sub.kind, sub.namespace, sub.name)] = sub
}

func (m *kubernetesMonitorV2) getSubscription(kind, namespace, name string) (*monitorSubscription, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub, ok := m.subs[m.subscriptionKey(kind, namespace, name)]
	return sub, ok
}

func (m *kubernetesMonitorV2) removeSubscription(sub *monitorSubscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.subs, m.subscriptionKey(sub.kind, sub.namespace, sub.name))
}

func (m *kubernetesMonitorV2) completeAndCleanup(sub *monitorSubscription) {
	m.removeSubscription(sub)
	containerruntime.UnmarkRuntimeMonitoring(sub.runtimeID)
}

func (m *kubernetesMonitorV2) subscriptionKey(kind, namespace, name string) string {
	return kind + "|" + namespace + "|" + name
}
