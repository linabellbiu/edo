package terminal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"edo/internal/terminalfiles"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func (s *Service) OpenKubernetesFiles(ctx context.Context, clusterID, namespace, podName, containerName, expectedUID string) (terminalfiles.Backend, error) {
	if s == nil || s.kube == nil || strings.TrimSpace(clusterID) == "" || len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(podName)) != 0 || len(validation.IsDNS1123Label(containerName)) != 0 {
		return nil, ErrInvalidRequest
	}
	if err := s.kube.RequireResourcePermission(ctx, clusterID, "pods", namespace, "execute", false); err != nil {
		return nil, err
	}
	lease, stop, err := s.kube.WithClusterAuthorizationLease(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	config, err := s.kube.RESTConfig(lease, clusterID)
	if err != nil {
		stop()
		return nil, fmt.Errorf("加载 Pod 文件连接失败: %w", err)
	}
	config = rest.CopyConfig(config)
	config.Timeout = 0
	api, err := kubernetes.NewForConfig(config)
	if err != nil {
		stop()
		return nil, fmt.Errorf("创建 Pod 文件客户端失败: %w", err)
	}
	pod, err := api.CoreV1().Pods(namespace).Get(lease, podName, metav1.GetOptions{})
	if err != nil {
		stop()
		return nil, fmt.Errorf("读取 Pod 文件目标失败: %w", err)
	}
	uid := string(pod.UID)
	if uid == "" || expectedUID != "" && uid != expectedUID {
		stop()
		return nil, terminalfiles.ErrConflict
	}
	containerIdentity := filePodContainerIdentity(pod, containerName)
	if containerIdentity == "" {
		stop()
		return nil, ErrTargetNotReady
	}
	f := &containerFiles{ctx: lease, cancel: stop, identity: "pod:" + uid + ":" + containerIdentity}
	f.check = func(op context.Context) error {
		if err := s.kube.RequireResourcePermission(op, clusterID, "pods", namespace, "execute", false); err != nil {
			return err
		}
		current, err := api.CoreV1().Pods(namespace).Get(op, podName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("核验 Pod 文件目标失败: %w", err)
		}
		if string(current.UID) != uid || filePodContainerIdentity(current, containerName) != containerIdentity {
			return terminalfiles.ErrConflict
		}
		return nil
	}
	f.run = func(op context.Context, command []string, input io.Reader, output io.Writer) error {
		request := api.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(podName).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: containerName, Command: command, Stdin: input != nil, Stdout: true, Stderr: true, TTY: false}, scheme.ParameterCodec)
		executor, err := remotecommand.NewSPDYExecutor(config, http.MethodPost, request.URL())
		if err != nil {
			return fmt.Errorf("创建 Pod 文件执行器失败: %w", err)
		}
		return fileExecError(executor.StreamWithContext(op, remotecommand.StreamOptions{Stdin: input, Stdout: output, Stderr: io.Discard, Tty: false}))
	}
	return f, nil
}

func filePodContainerIdentity(pod *corev1.Pod, name string) string {
	if pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !podHasContainer(pod, name) {
		return ""
	}
	statuses := append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.EphemeralContainerStatuses...)
	for _, status := range statuses {
		if status.Name == name && status.State.Running != nil && status.ContainerID != "" {
			return status.ContainerID
		}
	}
	return ""
}
