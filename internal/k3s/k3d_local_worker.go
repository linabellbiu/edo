package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"edo/internal/kube"
	"edo/internal/model"
)

type localK3dRevisionKey struct{}
type localK3dContainer struct {
	ID, Name          string
	Labels            map[string]string
	CPU, Memory, Swap int64
}

const localK3dInspectFormat = `{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Config.Labels}},"cpu":{{json .HostConfig.NanoCpus}},"memory":{{json .HostConfig.Memory}},"swap":{{json .HostConfig.MemorySwap}}}`

func localK3dNodeNames(input Input, id string) []string {
	names := []string{"k3d-" + k3dResourceName(id) + "-server-0"}
	for i := 0; i < k3dAgents(input.K3d); i++ {
		names = append(names, fmt.Sprintf("k3d-%s-agent-%d", k3dResourceName(id), i))
	}
	return names
}
func (r *localK3dRuntime) nodes(ctx context.Context, input Input, id string) ([]localK3dContainer, error) {
	names := localK3dNodeNames(input, id)
	args := append([]string{"inspect", "--format", localK3dInspectFormat}, names...)
	raw, _, err := r.read(ctx, "docker", args...)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != len(names) {
		return nil, ErrHostChanged
	}
	nodes := make([]localK3dContainer, len(names))
	for i, line := range lines {
		if json.Unmarshal([]byte(line), &nodes[i]) != nil || strings.TrimPrefix(nodes[i].Name, "/") != names[i] || nodes[i].Labels["edo.dev/installation"] != id || nodes[i].Labels["k3d.cluster"] != k3dResourceName(id) || len(nodes[i].ID) != 64 {
			return nil, ErrHostChanged
		}
		for _, ch := range nodes[i].ID {
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
				return nil, ErrHostChanged
			}
		}
	}
	return nodes, nil
}
func (r *localK3dRuntime) create(ctx context.Context, input Input, id string, out io.Writer) error {
	// 官方内存参数会挂载 CLI 本地生成的 meminfo；Docker 服务端可能不共享该文件系统。
	// 本地连接统一在官方创建返回后通过 Docker update 设置并核验限额，不依赖主机路径共享。
	args := k3dCreateArguments(input, id, false)
	if len(args) == 0 {
		return ErrInvalidInput
	}
	if err := r.available(ctx, input, ""); err != nil {
		return err
	}
	volume := k3dImageVolumeName(id)
	volumes, _, err := r.read(ctx, "docker", "volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	for _, name := range strings.Split(volumes, "\n") {
		if name == volume {
			return &ValidationError{Message: "本次 k3d 镜像卷名称已被占用，请重新生成安装计划"}
		}
	}
	if err := r.execute(ctx, r.tool.path, args, out); err != nil {
		return err
	}
	volumeInfo, _, err := r.read(ctx, "docker", "volume", "inspect", "--format", `{{.Name}} {{index .Labels "k3d.cluster"}}`, volume)
	if err != nil || strings.TrimSpace(volumeInfo) != volume+" "+k3dResourceName(id) {
		return ErrHostChanged
	}
	nodes, err := r.nodes(ctx, input, id)
	if err != nil {
		return err
	}
	server, worker := k3dResources(input.K3d)
	for i, node := range nodes {
		resource := worker
		if i == 0 {
			resource = server
		}
		args := []string{"update", "--cpus", k3dCPUArgument(resource.CPUMillis), "--memory", k3dMemoryArgument(resource.MemoryMiB), "--memory-swap", k3dMemoryArgument(resource.MemoryMiB), node.ID}
		if err := r.execute(ctx, "docker", args, out); err != nil {
			return err
		}
	}
	verified, err := r.nodes(ctx, input, id)
	if err != nil {
		return err
	}
	for i, node := range verified {
		resource := worker
		if i == 0 {
			resource = server
		}
		if node.ID != nodes[i].ID || node.CPU != resource.CPUMillis*1000000 || node.Memory != resource.MemoryMiB*1024*1024 || node.Swap != node.Memory {
			return ErrHostChanged
		}
	}
	return nil
}
func (r *localK3dRuntime) withLogs(ctx context.Context, input Input, id string, output io.Writer, operation func(context.Context) error) error {
	nodes, err := r.nodes(ctx, input, id)
	if err != nil {
		slog.WarnContext(ctx, "读取本地 k3d 日志目标失败", "operation", "k3d_local_logs_target", "err", err)
		_, _ = io.WriteString(output, "容器日志目标暂不可用，安装结果以就绪校验为准。\n")
		return operation(ctx)
	}
	observe, cancel := context.WithCancel(ctx)
	var group sync.WaitGroup
	for _, node := range nodes {
		group.Add(1)
		go func(node localK3dContainer) {
			defer group.Done()
			writer := &terminalLineWriter{output: output, prefix: "[" + strings.TrimPrefix(node.Name, "/") + "] ", redactor: newInstallationLogRedactor(nil)}
			err := r.execute(observe, "docker", []string{"logs", "--timestamps", "--tail", "200", "--follow", node.ID}, writer)
			_ = writer.flush()
			if err != nil && observe.Err() == nil {
				slog.WarnContext(ctx, "读取本地 k3d 启动日志失败", "operation", "k3d_local_logs", "container_id", node.ID, "err", err)
				_, _ = fmt.Fprintf(output, "[%s] 日志读取中断。\n", strings.TrimPrefix(node.Name, "/"))
			}
		}(node)
	}
	err = operation(ctx)
	cancel()
	group.Wait()
	return err
}
func (s *Service) performLocalK3d(ctx context.Context, record *model.K3sInstallation, snapshot savedInput) error {
	if snapshot.LocalK3d == nil || len(snapshot.HostDigests) != 1 || snapshot.Input.ServerHostID != model.BuiltinLocalHostID || k3dResourceName(record.ID) == "" {
		return ErrPreflightRequired
	}
	ctx = context.WithValue(ctx, localK3dRevisionKey{}, snapshot.LocalK3d)
	if ctx.Value(localK3dSessionKey{}) == nil {
		return s.localK3d.withCLI(ctx, func(ctx context.Context) error { return s.performLocalK3d(ctx, record, snapshot) })
	}

	input := snapshot.Input
	var hosts []Host
	if json.Unmarshal(record.Hosts, &hosts) != nil || len(hosts) != 1 || hosts[0].ID != model.BuiltinLocalHostID {
		return ErrInvalidInput
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	current, err := s.localK3d.describe(ctx)
	if err != nil {
		return err
	}
	if current.daemonID != snapshot.LocalK3d.DaemonID {
		return ErrHostChanged
	}
	report, err := s.localK3d.preflight(ctx, input, hosts)
	if err != nil {
		return err
	}
	if err := requirePreflight(report); err != nil {
		return err
	}
	if err := s.runHostOperation(ctx, record, hosts, 0, "k3d_tool", func(ctx context.Context, out io.Writer) error { return s.localK3d.tool.prepare(ctx, out) }); err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	// 下载后再次确认同一 Docker daemon，再由官方 CLI 创建本次独立资源。
	current, err = s.localK3d.describe(ctx)
	if err != nil {
		return err
	}
	if current.daemonID != snapshot.LocalK3d.DaemonID {
		return ErrHostChanged
	}
	if err := s.prepareK3dCreation(ctx, record, &snapshot); err != nil {
		return err
	}
	if err := s.runHostOperation(ctx, record, hosts, 0, "k3d_create", func(ctx context.Context, out io.Writer) error { return s.localK3d.create(ctx, input, record.ID, out) }); err != nil {
		return err
	}
	connectionRecord := *record
	logs := &terminalDeduplicator{}
	var value string
	if err := s.runHostOperation(ctx, record, hosts, 0, "api", func(ctx context.Context, out io.Writer) error {
		logs.output = out
		return s.localK3d.withLogs(ctx, input, record.ID, logs, func(ctx context.Context) error {
			var err error
			value, err = s.waitForK3dConnectionRead(ctx, &connectionRecord, snapshot, func(ctx context.Context) (string, error) {
				raw, _, err := s.localK3d.read(ctx, s.localK3d.tool.path, "kubeconfig", "get", k3dResourceName(record.ID))
				return raw, err
			})
			return err
		})
	}); err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	connectionInput := kube.Input{Name: input.Name, Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &value}
	readyRecord := *record
	if err := s.runHostOperation(ctx, record, hosts, 0, "node", func(ctx context.Context, out io.Writer) error {
		logs.output = out
		return s.localK3d.withLogs(ctx, input, record.ID, logs, func(ctx context.Context) error {
			return s.waitForK3dInput(ctx, &readyRecord, snapshot, connectionInput, 1+k3dAgents(input.K3d))
		})
	}); err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	return s.completeK3d(ctx, record, connectionInput, hosts)
}
