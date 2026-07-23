package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type kubernetesSession struct {
	input       *io.PipeWriter
	output      *io.PipeReader
	resizeQueue *terminalSizeQueue
	cancel      context.CancelFunc
	closeOnce   sync.Once
}

func (s *Service) OpenKubernetes(
	ctx context.Context,
	clusterID, namespace, podName, containerName, shell string,
	size Size,
) (Session, error) {
	if strings.TrimSpace(clusterID) == "" || len(validation.IsDNS1123Label(namespace)) > 0 ||
		len(validation.IsDNS1123Subdomain(podName)) > 0 || len(validation.IsDNS1123Label(containerName)) > 0 ||
		!validSize(size) {
		return nil, ErrInvalidRequest
	}
	command, err := normalizeShell(shell)
	if err != nil {
		return nil, err
	}
	restConfig, err := s.kube.RESTConfig(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("加载 Kubernetes 终端连接失败: %w", err)
	}
	restConfig = rest.CopyConfig(restConfig)
	restConfig.Timeout = 0
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("创建 Kubernetes 终端客户端失败: %w", err)
	}
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("读取 Kubernetes Pod 状态失败: %w", err)
	}
	if pod.Status.Phase != corev1.PodRunning || !podHasContainer(pod, containerName) {
		return nil, ErrTargetNotReady
	}
	request := clientset.CoreV1().RESTClient().Post().
		Resource("pods").Name(podName).Namespace(namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName, Command: command,
			Stdin: true, Stdout: true, Stderr: false, TTY: true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(restConfig, http.MethodPost, request.URL())
	if err != nil {
		return nil, fmt.Errorf("创建 Kubernetes Pod 终端执行器失败: %w", err)
	}

	sessionContext, cancel := context.WithCancel(ctx)
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	resizeQueue := newTerminalSizeQueue(sessionContext, size)
	session := &kubernetesSession{
		input: stdinWriter, output: stdoutReader, resizeQueue: resizeQueue, cancel: cancel,
	}
	go func() {
		err := executor.StreamWithContext(sessionContext, remotecommand.StreamOptions{
			Stdin: stdinReader, Stdout: stdoutWriter, Tty: true, TerminalSizeQueue: resizeQueue,
		})
		_ = stdinReader.CloseWithError(err)
		_ = stdoutWriter.CloseWithError(err)
	}()
	return session, nil
}

func (s *kubernetesSession) Read(buffer []byte) (int, error) {
	return s.output.Read(buffer)
}

func (s *kubernetesSession) Write(buffer []byte) (int, error) {
	return s.input.Write(buffer)
}

func (s *kubernetesSession) Resize(_ context.Context, size Size) error {
	if !validSize(size) {
		return ErrInvalidRequest
	}
	s.resizeQueue.Push(size)
	return nil
}

func (s *kubernetesSession) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		s.cancel()
		inputErr := s.input.Close()
		outputErr := s.output.Close()
		closeErr = errors.Join(inputErr, outputErr)
	})
	return closeErr
}

type terminalSizeQueue struct {
	ctx   context.Context
	sizes chan remotecommand.TerminalSize
}

func newTerminalSizeQueue(ctx context.Context, initial Size) *terminalSizeQueue {
	queue := &terminalSizeQueue{ctx: ctx, sizes: make(chan remotecommand.TerminalSize, 1)}
	queue.Push(initial)
	return queue
}

func (q *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case <-q.ctx.Done():
		return nil
	case size := <-q.sizes:
		return &size
	}
}

func (q *terminalSizeQueue) Push(size Size) {
	value := remotecommand.TerminalSize{Width: size.Columns, Height: size.Rows}
	select {
	case q.sizes <- value:
	default:
		select {
		case <-q.sizes:
		default:
		}
		select {
		case q.sizes <- value:
		case <-q.ctx.Done():
		}
	}
}

func podHasContainer(pod *corev1.Pod, containerName string) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name == containerName {
			return true
		}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		if container.Name == containerName {
			return true
		}
	}
	return false
}
