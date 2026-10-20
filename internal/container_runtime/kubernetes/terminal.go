package kubernetes

import (
	"context"
	"fmt"
	"io"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
)

// OpenTerminal 等价于 `kubectl exec -it <pod> -c main -- <command>`。
//
// 它通过 client-go 的 remotecommand（SPDY）在 Pod 内新建一个进程并申请 TTY，
// 不依赖容器内任何终端服务。
func (k *KubernetesRuntime) OpenTerminal(ctx context.Context, runtimeID string, opts containerruntime.TerminalOptions) (containerruntime.TerminalSession, error) {
	if k.restConfig == nil {
		return nil, fmt.Errorf("kubernetes rest config is not initialized")
	}
	meta, err := k.parseRuntimeID(runtimeID)
	if err != nil {
		return nil, err
	}

	podName, err := k.resolveLatestPodName(ctx, meta.Namespace, map[string]string{"gobrave-workload": meta.Name})
	if err != nil {
		return nil, err
	}

	pod, err := k.clientset.CoreV1().Pods(meta.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s: %w", podName, err)
	}
	if pod.Status.Phase != corev1.PodRunning {
		return nil, fmt.Errorf("pod %s is not running (phase=%s)", podName, pod.Status.Phase)
	}

	command := opts.Command
	if len(command) == 0 {
		command = []string{"/bin/bash"}
	}

	req := k.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(meta.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "main",
			Command:   command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       opts.TTY,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(k.restConfig, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("create exec executor for pod %s: %w", podName, err)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	session := &kubernetesTerminalSession{
		stdinReader:  nil,
		stdinWriter:  nil,
		stdoutReader: nil,
		stdoutWriter: nil,
		sizes:        make(chan remotecommand.TerminalSize, 4),
		done:         make(chan struct{}),
		cancel:       cancel,
	}
	session.stdinReader, session.stdinWriter = io.Pipe()
	session.stdoutReader, session.stdoutWriter = io.Pipe()

	if opts.Rows > 0 && opts.Cols > 0 {
		session.Resize(opts.Rows, opts.Cols)
	}

	go func() {
		defer close(session.done)
		streamErr := exec.StreamWithContext(sessionCtx, remotecommand.StreamOptions{
			Stdin:             session.stdinReader,
			Stdout:            session.stdoutWriter,
			Stderr:            session.stdoutWriter,
			Tty:               opts.TTY,
			TerminalSizeQueue: session,
		})
		_ = session.stdoutWriter.CloseWithError(streamErr)
		_ = session.stdinReader.CloseWithError(streamErr)
	}()

	return session, nil
}

type kubernetesTerminalSession struct {
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter

	sizes chan remotecommand.TerminalSize
	done  chan struct{}
	once  sync.Once
	// cancel 终止 StreamWithContext。
	cancel context.CancelFunc
}

func (s *kubernetesTerminalSession) Read(p []byte) (int, error) {
	return s.stdoutReader.Read(p)
}

func (s *kubernetesTerminalSession) Write(p []byte) (int, error) {
	return s.stdinWriter.Write(p)
}

func (s *kubernetesTerminalSession) Resize(rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return nil
	}
	select {
	case s.sizes <- remotecommand.TerminalSize{Width: cols, Height: rows}:
	default:
		// 丢弃来不及消费的 resize，避免阻塞 WebSocket 读循环。
	}
	return nil
}

// Next 实现 remotecommand.TerminalSizeQueue。
func (s *kubernetesTerminalSession) Next() *remotecommand.TerminalSize {
	select {
	case size := <-s.sizes:
		return &size
	case <-s.done:
		return nil
	}
}

func (s *kubernetesTerminalSession) Close() error {
	s.once.Do(func() {
		s.cancel()
		_ = s.stdinWriter.Close()
		_ = s.stdoutReader.Close()
	})
	return nil
}
