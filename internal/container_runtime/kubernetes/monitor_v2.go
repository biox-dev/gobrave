package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/logger"
)

// jobLiveReadTimeout bounds the one live API read taken when a terminal job event
// is emitted. The read is the evidence that the event was not stale, so it must
// not be allowed to hang a monitor goroutine.
const jobLiveReadTimeout = 3 * time.Second

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
		// Anchor line: it pairs with the terminal-event evidence lines below, so a
		// container's whole observed lifecycle can be read from one runtime_id.
		logger.Debugf(context.Background(), "[KubernetesMonitorV2] job monitoring registered, runtime_id=%s namespace=%s name=%s", runtimeID, meta.Namespace, meta.Name)
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
	// startedAt is when the workload was first observed running. It exists so a
	// terminal event can state how long the workload actually ran: a workload
	// reported as finished milliseconds after it started is the fingerprint of a
	// premature or stale terminal event, and that is invisible in a log which only
	// shows "ContainerExited".
	startedAt time.Time
}

func (s *monitorState) emitStarted(emit func()) {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.startedAt = time.Now()
	s.mu.Unlock()
	emit()
}

// ranFor reports how long the workload has been observed running, and whether a
// start was observed at all. A terminal event with no observed start, or with a
// run time of milliseconds, cannot have come from a container that executed its
// script.
func (s *monitorState) ranFor() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.startedAt.IsZero() {
		return 0, false
	}
	return time.Since(s.startedAt), true
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
		go m.logTerminalEvidence(sub, "informer(deleted)", "ContainerDeleted", job, "job not found")
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
			logger.Debugf(context.Background(), "[KubernetesMonitorV2] ContainerStarted emitted, runtime_id=%s source=informer %s", sub.runtimeID, jobFacts(job))
		})
	}
	// Terminating 也会触发 update 事件
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		if !jobHasStarted(job) {
			// A suspended job that never started is not an exit: jobs are created
			// suspended (see createJob) and unsuspended by Start, so this is the
			// pre-start object an informer delta or snapshot can still carry after
			// the subscription was registered. Reporting it as ContainerExited
			// (exit 0) told the owner the container had finished successfully
			// within milliseconds of being created, while the real container was
			// still running - which surfaced as an intermittent "output validation
			// failed: missing output" node failure. Wait for the actual outcome
			// instead; a stop requested before the job ever started is converged by
			// the stop sweep, not by an exit event.
			logger.Debugf(context.Background(), "[KubernetesMonitorV2] ignored a suspended job that never started (pre-start state, not an exit), runtime_id=%s source=informer %s", sub.runtimeID, jobFacts(job))
			return
		}
		if sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerExited", sub.runtimeID, "0")
			go m.logTerminalEvidence(sub, "informer(suspend-after-start)", "ContainerExited", job, "0")
		}) {
			m.completeAndCleanup(sub)
		}
		return
	}
	if job.Status.Succeeded > 0 {
		if sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerExited", sub.runtimeID, "0")
			go m.logTerminalEvidence(sub, "informer(succeeded)", "ContainerExited", job, "0")
		}) {
			m.completeAndCleanup(sub)
		}
		return
	}
	if job.Status.Failed > 0 {
		message := jobFailureMessage(job)
		if sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerFailed", sub.runtimeID, message)
			go m.logTerminalEvidence(sub, "informer(failed)", "ContainerFailed", job, message)
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
			logger.Debugf(context.Background(), "[KubernetesMonitorV2] ContainerStarted emitted, runtime_id=%s source=snapshot %s", sub.runtimeID, jobFacts(job))
		})
	}
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		// Same guard as handleJobEvent: a job that is suspended because it has not
		// started yet is not a finished run.
		if !jobHasStarted(job) {
			logger.Debugf(context.Background(), "[KubernetesMonitorV2] ignored a suspended job that never started (pre-start state, not an exit), runtime_id=%s source=snapshot %s", sub.runtimeID, jobFacts(job))
			return false
		}
		return sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerExited", sub.runtimeID, "0")
			go m.logTerminalEvidence(sub, "snapshot(suspend-after-start)", "ContainerExited", job, "0")
		})
	}
	if job.Status.Succeeded > 0 {
		return sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerExited", sub.runtimeID, "0")
			go m.logTerminalEvidence(sub, "snapshot(succeeded)", "ContainerExited", job, "0")
		})
	}
	if job.Status.Failed > 0 {
		message := jobFailureMessage(job)
		return sub.state.emitAndClose(func() {
			m.runtime.emitEvent("ContainerFailed", sub.runtimeID, message)
			go m.logTerminalEvidence(sub, "snapshot(failed)", "ContainerFailed", job, message)
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

// jobFacts renders the job fields that decide whether a lifecycle event is real.
//
// It is attached to every job lifecycle log line so a runtime event can be audited
// from the log alone: a genuine exit shows succeeded/failed>0 with a completion
// time, while a premature or stale event shows the workload still active - or still
// suspended - with no completion time at all.
func jobFacts(job *batchv1.Job) string {
	if job == nil {
		return "job=nil"
	}
	suspend := "unset"
	if job.Spec.Suspend != nil {
		suspend = strconv.FormatBool(*job.Spec.Suspend)
	}
	age := "unknown"
	if !job.CreationTimestamp.IsZero() {
		age = time.Since(job.CreationTimestamp.Time).Round(time.Millisecond).String()
	}
	return fmt.Sprintf("job_suspend=%s job_active=%d job_succeeded=%d job_failed=%d job_start_time=%s job_completion_time=%s job_age=%s",
		suspend, job.Status.Active, job.Status.Succeeded, job.Status.Failed,
		formatJobTime(job.Status.StartTime), formatJobTime(job.Status.CompletionTime), age)
}

func formatJobTime(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return "none"
	}
	return t.UTC().Format(time.RFC3339)
}

// liveJobFacts reads the job straight from the API server, bypassing the informer
// cache, and reports its facts together with whether the workload is still doing
// work. The live read is what makes the log conclusive: when it still shows an
// active, unsuspended job, the terminal event that was just emitted was premature,
// so the container must not be treated as finished and no output should be expected
// yet.
//
// A suspended job with active pods is NOT "still working" - that is the normal
// shape of a stop in progress (the pod is terminating), so it must not be reported
// as a premature event.
func (m *kubernetesMonitorV2) liveJobFacts(sub *monitorSubscription) (string, bool) {
	if m == nil || m.runtime == nil || m.runtime.clientset == nil || sub == nil {
		return "unavailable", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobLiveReadTimeout)
	defer cancel()
	job, err := m.runtime.clientset.BatchV1().Jobs(sub.namespace).Get(ctx, sub.name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "not_found", false
		}
		return fmt.Sprintf("read_failed=%v", err), false
	}
	suspended := job.Spec.Suspend != nil && *job.Spec.Suspend
	stillRunning := job.Status.Active > 0 && job.Status.Succeeded == 0 && job.Status.Failed == 0 && !suspended
	return jobFacts(job), stillRunning
}

// logTerminalEvidence records why a terminal runtime event was emitted: what the
// object carrying it said, how long the workload was observed running, and what the
// API server still reports. It is started in its own goroutine because it performs
// a live API read and the informer handler calling it is shared by every
// subscription - evidence gathering must never delay another container's events.
//
// This is the line that separates "the container really finished, so a missing
// outputs.json is a container or filesystem problem" from "the workload was still
// running, so the exit event was premature and the node must not be completed".
//
// Log level: kept at info/warn on purpose. It is emitted once per container and is
// the only record that explains a missing-output failure after the fact, so it has
// to survive a log level of info. Everything that repeats per informer delta
// (registration, ContainerStarted, ignored pre-start jobs) is debug instead.
func (m *kubernetesMonitorV2) logTerminalEvidence(sub *monitorSubscription, source string, event string, job *batchv1.Job, message string) {
	if m == nil || sub == nil {
		return
	}
	ranFor, startObserved := "unobserved", false
	if sub.state != nil {
		if d, ok := sub.state.ranFor(); ok {
			ranFor, startObserved = d.Round(time.Millisecond).String(), true
		}
	}

	liveFacts, liveStillRunning := m.liveJobFacts(sub)
	ctx := context.Background()
	if liveStillRunning {
		logger.Warnf(ctx, "[KubernetesMonitorV2] terminal event emitted while the live job is STILL ACTIVE (premature/stale event), runtime_id=%s event=%s source=%s message=%q ran_for=%s start_observed=%t %s live=%s",
			sub.runtimeID, event, source, message, ranFor, startObserved, jobFacts(job), liveFacts)
		return
	}
	logger.Infof(ctx, "[KubernetesMonitorV2] terminal event emitted, runtime_id=%s event=%s source=%s message=%q ran_for=%s start_observed=%t %s live=%s",
		sub.runtimeID, event, source, message, ranFor, startObserved, jobFacts(job), liveFacts)
}
