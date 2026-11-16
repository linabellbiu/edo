package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/sshclient"
)

var localDockerArchitecture = regexp.MustCompile(`^[a-z0-9]+$`)

type localK3dSnapshot struct {
	Revision string `json:"revision"`
	DaemonID string `json:"daemon_id"`
}
type LocalK3dInfo struct {
	BindAddress        string `json:"bind_address"`
	APIHost            string `json:"api_host"`
	Architecture       string `json:"architecture"`
	revision, daemonID string
	cpus               int64
	memory             int64
	runcVersion        string
}
type localDockerCLI interface {
	LocalCLIEnvironment(context.Context) ([]string, string, func(), error)
}
type localK3dRuntime struct {
	docker     localDockerCLI
	runCommand func(context.Context, string, []string, []string, io.Writer, io.Writer) error
	tool       *localK3dTool
}

func newLocalK3dRuntime(docker localDockerCLI) *localK3dRuntime {
	return &localK3dRuntime{docker: docker, runCommand: runLocalK3dCommand, tool: newLocalK3dTool()}
}
func runLocalK3dCommand(ctx context.Context, binary string, args, environment []string, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = environment
	command.WaitDelay = 2 * time.Second
	command.Stdout, command.Stderr = stdout, stderr
	return command.Run()
}

type localK3dSessionKey struct{}
type localK3dSession struct {
	environment []string
	revision    string
}

func (r *localK3dRuntime) withCLI(ctx context.Context, operation func(context.Context) error) error {
	if ctx.Value(localK3dSessionKey{}) != nil {
		return operation(ctx)
	}
	if r == nil || r.docker == nil {
		return ErrRemote
	}
	env, revision, cleanup, err := r.docker.LocalCLIEnvironment(ctx)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		slog.ErrorContext(ctx, "读取本地 Docker 会话失败", "operation", "k3d_local_session", "err", err)
		return &ValidationError{Message: "本地 Docker 连接不可用，请检查本地主机的 Docker 能力与 EDO 的 Docker 配置"}
	}
	if expected, _ := ctx.Value(localK3dRevisionKey{}).(*localK3dSnapshot); expected != nil && expected.Revision != revision {
		return ErrHostChanged
	}
	return operation(context.WithValue(ctx, localK3dSessionKey{}, &localK3dSession{environment: env, revision: revision}))
}
func (r *localK3dRuntime) command(ctx context.Context, binary string, args []string, stdout, stderr io.Writer) (string, error) {
	if r == nil || r.docker == nil {
		return "", ErrRemote
	}
	session, _ := ctx.Value(localK3dSessionKey{}).(*localK3dSession)
	if session == nil {
		var revision string
		err := r.withCLI(ctx, func(ctx context.Context) error {
			var err error
			revision, err = r.command(ctx, binary, args, stdout, stderr)
			return err
		})
		return revision, err
	}
	environment, revision := session.environment, session.revision
	if expected, _ := ctx.Value(localK3dRevisionKey{}).(*localK3dSnapshot); expected != nil && expected.Revision != revision {
		return revision, ErrHostChanged
	}
	if err := r.runCommand(ctx, binary, args, environment, stdout, stderr); err != nil {
		if ctx.Err() != nil {
			return revision, ctx.Err()
		}
		slog.ErrorContext(ctx, "本地 k3d 命令执行失败", "operation", "k3d_local_command", "binary", binary, "err", err)
		if errors.Is(err, exec.ErrNotFound) {
			return revision, &ValidationError{Message: "EDO 后端未找到 Docker 命令，请检查 Docker 运行环境"}
		}
		return revision, ErrRemote
	}
	return revision, nil
}
func (r *localK3dRuntime) read(ctx context.Context, binary string, args ...string) (string, string, error) {
	output, diagnostic := &limitedOutput{limit: 1024 * 1024}, &limitedOutput{limit: 64 * 1024}
	revision, err := r.command(ctx, binary, args, output, diagnostic)
	if err != nil {
		redactor := newInstallationLogRedactor(nil)
		feedback := ""
		for _, line := range strings.Split(diagnostic.buffer.String(), "\n") {
			if safe := redactor.line(line); safe != "" {
				feedback = appendBoundedInstallationLog(feedback, safe+"\n", 8192)
			}
		}
		slog.ErrorContext(ctx, "读取本地 k3d 命令结果失败", "operation", "k3d_local_read", "binary", binary, "diagnostic", feedback, "err", err)
	}
	if output.exceeded {
		return "", revision, ErrRemote
	}
	return output.buffer.String(), revision, err
}
func (r *localK3dRuntime) execute(ctx context.Context, binary string, args []string, output io.Writer) error {
	stdout, stderr := &terminalLineWriter{output: output}, &terminalLineWriter{output: output}
	_, err := r.command(ctx, binary, args, stdout, stderr)
	_ = stdout.flush()
	_ = stderr.flush()
	return err
}
func (r *localK3dRuntime) describe(ctx context.Context) (LocalK3dInfo, error) {
	if ctx.Value(localK3dSessionKey{}) == nil {
		var info LocalK3dInfo
		err := r.withCLI(ctx, func(ctx context.Context) error { var err error; info, err = r.describe(ctx); return err })
		return info, err
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	value, revision, err := r.read(ctx, "docker", "info", "--format", "{{json .}}")
	if err != nil {
		if ValidationMessage(err) != "" {
			return LocalK3dInfo{}, err
		}
		return LocalK3dInfo{}, &ValidationError{Message: "无法连接本地 Docker，请检查本地主机的 Docker 能力与 EDO 的 Docker 配置"}
	}
	var info struct {
		ID, Architecture, OSType, ServerVersion string
		NCPU                                    int64
		MemTotal                                int64
	}
	if json.Unmarshal([]byte(value), &info) != nil || info.ID == "" || info.OSType != "linux" || !localDockerVersionSupported(info.ServerVersion) {
		slog.ErrorContext(ctx, "本地 Docker 信息不满足 k3d 要求", "operation", "k3d_local_info", "os", info.OSType, "version", info.ServerVersion, "err", ErrInvalidInput)
		return LocalK3dInfo{}, &ValidationError{Message: "本地 k3d 需要 Linux Docker 20.10.5 或以上"}
	}
	versionData, _, err := r.read(ctx, "docker", "version", "--format", "{{json .Server}}")
	var server struct {
		Arch       string
		Components []struct{ Name, Version string }
	}
	if err != nil || json.Unmarshal([]byte(versionData), &server) != nil || !localDockerArchitecture.MatchString(server.Arch) {
		return LocalK3dInfo{}, &ValidationError{Message: "无法确认本地 Docker 的实际 CPU 架构，请重新检测"}
	}
	architecture := server.Arch
	runcVersion := ""
	for _, component := range server.Components {
		if component.Name == "runc" {
			runcVersion = component.Version
		}
	}
	session := ctx.Value(localK3dSessionKey{}).(*localK3dSession)
	env := session.environment
	dockerHost := ""
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == "DOCKER_HOST" {
			dockerHost = value
		}
	}
	parsed, err := url.Parse(dockerHost)
	if err != nil {
		return LocalK3dInfo{}, ErrHostChanged
	}
	result := LocalK3dInfo{BindAddress: "127.0.0.1", APIHost: "127.0.0.1", Architecture: architecture, revision: revision, daemonID: info.ID, cpus: info.NCPU, memory: info.MemTotal, runcVersion: runcVersion}
	switch parsed.Scheme {
	case "tcp", "http", "https":
		result.BindAddress = "0.0.0.0"
		result.APIHost = parsed.Hostname()
	case "unix", "npipe":
		if _, err := os.Stat("/.dockerenv"); err == nil {
			result.BindAddress = "0.0.0.0"
			result.APIHost = ""
			if addresses, err := net.DefaultResolver.LookupHost(ctx, "host.docker.internal"); err == nil && len(addresses) > 0 {
				result.APIHost = "host.docker.internal"
			}
		}
	default:
		return LocalK3dInfo{}, &ValidationError{Message: "本地 k3d 暂不支持当前 Docker 连接协议"}
	}

	return result, nil
}
func localDockerVersionSupported(version string) bool {
	var major, minor, patch int
	if n, _ := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); n != 3 {
		return false
	}
	return major > 20 || major == 20 && (minor > 10 || minor == 10 && patch >= 5)
}
func (r *localK3dRuntime) checkRevision(ctx context.Context, expected *localK3dSnapshot) error {
	if expected == nil || r == nil || r.docker == nil {
		return ErrHostChanged
	}
	_, revision, cleanup, err := r.docker.LocalCLIEnvironment(ctx)
	if cleanup != nil {
		cleanup()
	}
	if err != nil || revision != expected.Revision {
		slog.WarnContext(ctx, "本地 k3d Docker 连接身份变化", "operation", "k3d_local_revision", "err", err)
		return ErrHostChanged
	}
	return nil
}
func (s *Service) loadInstallationHost(ctx context.Context, mode, id string) (model.Host, sshclient.Bundle, error) {
	if mode != "k3d" || id != model.BuiltinLocalHostID {
		return s.loadHost(ctx, id)
	}
	var host model.Host
	if err := s.db.WithContext(ctx).First(&host, "uuid = ?", id).Error; err != nil {
		slog.WarnContext(ctx, "读取本地 k3d 主机失败", "operation", "k3d_local_host", "host_id", id, "err", err)
		return host, sshclient.Bundle{}, ErrHostChanged
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if host.ProjectID != scope.ProjectID || host.DepartmentID != scope.DepartmentID || !host.IsActive || !host.IsBuiltin || host.Mode != model.HostModeLocal {
		return host, sshclient.Bundle{}, ErrHostChanged
	}
	var capability model.HostCapability
	if err := s.db.WithContext(ctx).First(&capability, "host_id = ? AND kind = ? AND status = ?", id, model.HostCapabilityDocker, model.HostCapabilityReady).Error; err != nil {
		slog.WarnContext(ctx, "本地主机 Docker 能力未就绪", "operation", "k3d_local_capability", "host_id", id, "err", err)
		return host, sshclient.Bundle{}, &ValidationError{Message: "本地主机的 Docker 能力尚未就绪，请先检查 Docker 连接"}
	}
	return host, sshclient.Bundle{}, nil
}
func (s *Service) LocalK3dInfo(ctx context.Context, actorID string) (LocalK3dInfo, error) {
	if _, err := s.authorize(ctx, actorID, "create"); err != nil {
		return LocalK3dInfo{}, err
	}
	if _, _, err := s.loadInstallationHost(ctx, "k3d", model.BuiltinLocalHostID); err != nil {
		return LocalK3dInfo{}, err
	}
	return s.localK3d.describe(ctx)
}
func k3dCreateArguments(input Input, id string, withMemoryMounts bool) []string {
	if input.K3d == nil || k3dResourceName(id) == "" || validateK3dResources(input.K3d) != nil {
		return nil
	}
	endpoint, _ := url.Parse(input.Endpoint)
	args := []string{"cluster", "create", k3dResourceName(id), "--servers", "1", "--agents", strconv.Itoa(k3dAgents(input.K3d)), "--api-port", net.JoinHostPort(input.K3d.BindAddress, strconv.Itoa(input.K3d.APIPort)), "--image", k3dImage(input.K3d)}
	if withMemoryMounts {
		server, worker := k3dResources(input.K3d)
		args = append(args, "--servers-memory", k3dMemoryArgument(server.MemoryMiB), "--agents-memory", k3dMemoryArgument(worker.MemoryMiB))
	}
	return append(args, "--verbose", "--no-lb", "--no-rollback", "--wait=false", "--timeout", "5m", "--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "--runtime-label", "edo.dev/installation="+id+"@all", "--k3s-node-label", "edo.dev/installation="+id+"@all", "--k3s-arg", "--tls-san="+endpoint.Hostname()+"@server:0")
}

func (r *localK3dRuntime) preflight(ctx context.Context, input Input, hosts []Host) (*PreflightReport, error) {
	if ctx.Value(localK3dSessionKey{}) == nil {
		var report *PreflightReport
		err := r.withCLI(ctx, func(ctx context.Context) error {
			var err error
			report, err = r.preflight(ctx, input, hosts)
			return err
		})
		return report, err
	}

	if len(hosts) != 1 || input.K3d == nil {
		return nil, ErrInvalidInput
	}
	host := HostPreflight{HostID: hosts[0].ID, HostName: hosts[0].Name, Role: "host", Hostname: "", Passed: true, Checks: []PreflightCheck{}}
	add := func(id, name, status, message string) {
		host.Checks = append(host.Checks, check(id, name, status, message))
		if status == "failed" {
			host.Passed = false
		}
	}
	info, err := r.describe(ctx)
	if err != nil {
		add("docker", "本地 Docker", "failed", ValidationMessage(err))
		return &PreflightReport{CheckedAt: time.Now().UTC(), Hosts: []HostPreflight{host}, Checks: []PreflightCheck{}}, nil
	}
	add("docker", "本地 Docker", "passed", "已连接 EDO 配置的 Linux Docker")
	add("privilege", "Docker 操作权限", "passed", "使用 EDO 已配置的 Docker 连接，无需 SSH 或 sudo")
	if supported, known := localRuncSupported(info.runcVersion); !known {
		add("runc", "容器运行时版本", "warning", "Docker 未提供可核对的 runc 版本，未将此项视为通过")
	} else if !supported {
		add("runc", "容器运行时版本", "failed", "k3d 需要 runc 1.0.0-rc93 或以上，请检查 Docker 运行环境")
	} else {
		add("runc", "容器运行时版本", "passed", "runc 版本满足官方要求")
	}

	server, worker := k3dResources(input.K3d)
	cpu := (server.CPUMillis + worker.CPUMillis*int64(k3dAgents(input.K3d)) + 999) / 1000
	memoryMiB := server.MemoryMiB + worker.MemoryMiB*int64(k3dAgents(input.K3d)) + 512
	if info.cpus < cpu {
		add("cpu", "累计 CPU", "failed", "Docker 可用 CPU 容量不足，请减少节点或降低 CPU 配置")
	} else {
		add("cpu", "累计 CPU", "passed", "Docker CPU 容量满足配置")
	}
	if info.memory/(1024*1024) < memoryMiB {
		add("memory", "累计内存", "failed", "Docker 内存容量不足，请减少节点或降低内存配置")
	} else {
		add("memory", "累计内存", "passed", "Docker 内存容量满足节点配置及 512 MiB 预留")
	}
	add("capacity", "当前空闲资源", "warning", "Docker 接口未提供当前空闲内存与磁盘；上述结果只确认 Docker 总容量，未视为空闲资源已通过")
	if err := r.available(ctx, input, ""); err != nil {
		add("existing", "集群与 API 端口", "failed", ValidationMessage(err))
	} else {
		add("existing", "集群与 API 端口", "passed", "没有已占用的 k3d 集群或 Docker 发布端口")
	}
	toolLog, err := r.tool.check(ctx)
	host.Logs = toolLog
	if err != nil {
		add("download", "k3d 工具", "failed", "官方 k3d 工具不可用，请查看执行日志并重新检测")
	} else {
		add("download", "k3d 工具", "passed", "官方固定版本工具可用或下载源可达")
	}
	add("component_images", "系统组件镜像", "warning", "检测不拉取镜像，实际拉取结果以安装输出为准")
	return &PreflightReport{CheckedAt: time.Now().UTC(), Passed: host.Passed, Checks: []PreflightCheck{}, Hosts: []HostPreflight{host}}, ctx.Err()
}
func (r *localK3dRuntime) available(ctx context.Context, input Input, ownID string) error {
	value, _, err := r.read(ctx, "docker", "container", "ls", "--all", "--format", "{{json .}}")
	if err != nil {
		return &ValidationError{Message: "无法读取本地 Docker 集群与端口占用情况"}
	}
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		if line == "" {
			continue
		}
		var row struct{ Names, Labels, Ports string }
		if json.Unmarshal([]byte(line), &row) != nil {
			return ErrRemote
		}
		labels := map[string]string{}
		for _, pair := range strings.Split(row.Labels, ",") {
			key, value, _ := strings.Cut(pair, "=")
			labels[key] = value
		}
		if labels["k3d.cluster"] != "" && (ownID == "" || labels["edo.dev/installation"] != ownID) {
			return &ValidationError{Message: "本地主机已有 k3d 集群，请先使用现有集群；一台主机只能安装一个集群"}
		}
		if labels["edo.dev/installation"] == ownID && ownID != "" {
			continue
		}
		if strings.Contains(row.Ports, ":"+strconv.Itoa(input.K3d.APIPort)+"->") {
			return &ValidationError{Message: "本地 Docker 的 API 端口已被占用，请更换端口"}
		}
	}
	return nil
}

func localRuncSupported(value string) (bool, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	base, _, _ := strings.Cut(value, "+")
	number, pre, prerelease := strings.Cut(base, "-")
	var major, minor, patch int
	if count, _ := fmt.Sscanf(number, "%d.%d.%d", &major, &minor, &patch); count != 3 {
		return false, false
	}
	if major > 1 || major == 1 && (minor > 0 || patch > 0) {
		return true, true
	}
	if major < 1 {
		return false, true
	}
	if !prerelease {
		return true, true
	}
	if rc, ok := strings.CutPrefix(pre, "rc"); ok {
		number, err := strconv.Atoi(rc)
		if err == nil {
			return number >= 93, true
		}
	}
	return false, false
}
