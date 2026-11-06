package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/google/uuid"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

var ErrContainerRecreateBaseline = errors.New("原发布配置或镜像已变化，请重新发布后再重建")
var ErrContainerControlBusy = errors.New("该 Compose 项目正在执行其他操作，请稍后重试")

type ContainerControlConfirmation struct {
	Digest string `json:"digest"`
}

type ContainerControlResult struct {
	Runtime *RuntimeState
	Plan    *dockerengine.ComposeContainerControlPlan
}

func ContainerControlPermission(action string) string {
	switch action {
	case "restart":
		return access.PermissionContainerRestart
	case "stop":
		return access.PermissionContainerStop
	case "recreate":
		return access.PermissionContainerRecreate
	}
	return ""
}

func (s *Service) requireComposeRuntimeActionPermission(ctx context.Context, record *model.DeploymentRecord, actorID, action string) error {
	if record.DeploymentPlanKind != model.DeploymentPlanComposeStack {
		return nil
	}
	permission := ContainerControlPermission(action)
	if permission == "" {
		return ErrRuntimeControlInvalid
	}
	return s.requireCurrentActorPermission(ctx, record, actorID, permission)
}

func (s *Service) requireContainerControl(ctx context.Context, record *model.DeploymentRecord, actorID, action string) error {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.UserID != actorID || ContainerControlPermission(action) == "" {
		return s.executionAuthorizationDenied(ctx, record, "container_control_actor", actorID, ContainerControlPermission(action), ErrRuntimeControlInvalid)
	}
	for _, permission := range []string{access.PermissionDeploymentRead, ContainerControlPermission(action)} {
		if err := s.requireCurrentActorPermission(ctx, record, actorID, permission); err != nil {
			return err
		}
	}
	if action == "recreate" && record.UsesDeliveryVariables {
		return s.requireCurrentActorPermission(ctx, record, actorID, "delivery_variable.execute")
	}
	return nil
}

func (s *Service) ContainerControlPreview(ctx context.Context, deploymentID, containerID, actorID, action string) (*dockerengine.ComposeContainerControlPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if ContainerControlPermission(action) == "" || len(containerID) != 64 {
		s.logger.Warn("容器运行操作参数无效", "operation", "container_control_preview", "deployment_id", deploymentID, "action", action)
		return nil, ErrRuntimeControlInvalid
	}
	record, err := s.currentDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if err = s.requireContainerControl(ctx, record, actorID, action); err != nil {
		return nil, err
	}
	endpoint, scope, err := s.composeRuntimeScope(ctx, record)
	if err != nil {
		return nil, s.containerControlError(record, containerID, action, err)
	}
	plan, err := s.docker.ComposeContainerControlPlan(ctx, endpoint, scope, containerID, action)
	if err != nil {
		return nil, s.containerControlError(record, containerID, action, err)
	}
	if action == "recreate" {
		if _, err = frozenComposeContent(record); err != nil {
			return nil, s.containerControlError(record, containerID, action, err)
		}
	}
	return plan, nil
}

func (s *Service) ControlContainer(ctx context.Context, deploymentID, containerID, actorID, action string, confirmation ContainerControlConfirmation) (*ContainerControlResult, error) {
	if len(confirmation.Digest) != 64 {
		s.logger.Warn("容器操作缺少有效确认", "operation", "container_control_validate", "deployment_id", deploymentID, "container_id", containerID, "action", action)
		return nil, ErrRuntimeControlInvalid
	}
	plan, err := s.ContainerControlPreview(ctx, deploymentID, containerID, actorID, action)
	if err != nil {
		return nil, err
	}
	if plan.Digest != confirmation.Digest {
		return nil, dockerengine.ErrContainerControlChanged
	}
	record, err := s.currentDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	lock, err := s.acquireTargetLock(ctx, record)
	if err != nil {
		return nil, s.containerControlError(record, containerID, action, err)
	}
	if lock != nil {
		defer func() {
			release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := lock.Release(release); err != nil {
				s.logger.Error("释放容器操作锁失败", "operation", "container_control_unlock", "deployment_id", deploymentID, "err", err)
			}
		}()
	}
	// 锁内再次读取当前部署、权限及确认范围，旧确认和重复请求不能复用。
	record, err = s.currentDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if err = s.requireContainerControl(ctx, record, actorID, action); err != nil {
		return nil, err
	}
	endpoint, scope, err := s.composeRuntimeScope(ctx, record)
	if err != nil {
		return nil, s.containerControlError(record, containerID, action, err)
	}
	plan, err = s.docker.ComposeContainerControlPlan(ctx, endpoint, scope, containerID, action)
	if err != nil {
		return nil, s.containerControlError(record, containerID, action, err)
	}
	if plan.Digest != confirmation.Digest {
		return nil, dockerengine.ErrContainerControlChanged
	}
	timeout := time.Duration(record.RolloutTimeout) * time.Second
	if timeout < 30*time.Second || timeout > time.Hour {
		timeout = 5 * time.Minute
	}
	execution, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var result dockerengine.RuntimeState
	if action == "recreate" {
		result, err = s.recreateComposeService(execution, record, endpoint, scope, plan, actorID, timeout)
	} else {
		result, err = s.docker.ControlComposeContainer(execution, endpoint, scope, containerID, action, plan.Digest, func() error { return s.requireContainerControl(execution, record, actorID, action) })
	}
	if err != nil {
		return &ContainerControlResult{Plan: plan}, s.containerControlError(record, containerID, action, err)
	}
	s.logger.Info("容器运行操作完成", "operation", "container_control", "deployment_id", deploymentID, "container_id", containerID, "service", plan.Service, "action", action, "actor_id", actorID, "affected_count", len(plan.Containers))
	return &ContainerControlResult{Runtime: dockerRuntimeState(deploymentID, result), Plan: plan}, nil
}

func (s *Service) containerControlError(record *model.DeploymentRecord, containerID, action string, cause error) error {
	s.logger.Error("容器运行操作失败", "operation", "container_control", "deployment_id", record.ID, "target_id", record.TargetID, "container_id", containerID, "action", action, "err", cause)
	if errors.Is(cause, dockerengine.ErrContainerControlChanged) {
		return dockerengine.ErrContainerControlChanged
	}
	if errors.Is(cause, ErrExecutionAuthorization) {
		return ErrExecutionAuthorization
	}
	if errors.Is(cause, ErrContainerRecreateBaseline) {
		return ErrContainerRecreateBaseline
	}
	if errors.Is(cause, ErrContainerControlBusy) {
		return ErrContainerControlBusy
	}
	if errors.Is(cause, ErrRuntimeControlUnsupported) {
		return ErrRuntimeControlUnsupported
	}
	return ErrRuntimeControlFailed
}

// frozenComposeContent 仅解码 EDO 自身生成的定界格式，并同时核验整个命令快照及正文摘要。
// 不执行历史脚本，不解析或接收用户提供的 Shell，也不从当前可编辑方案恢复配置。
func frozenComposeContent(record *model.DeploymentRecord) (string, error) {
	if record.CommandDigest == "" || record.CommandDigest != model.DeploymentPlanExecutionDigest(model.DeploymentPlanComposeStack, record.CommandScript, record.CommandTimeout) {
		return "", ErrRuntimeControlUnsupported
	}
	prefix := "cat >\"$release/compose.yml\" <<'EDO_COMPOSE_"
	if strings.Count(record.CommandScript, prefix) != 1 {
		return "", ErrRuntimeControlUnsupported
	}
	_, tail, _ := strings.Cut(record.CommandScript, prefix)
	marker, body, ok := strings.Cut(tail, "'\n")
	digest := strings.TrimRight(marker, "_")
	if !ok || len(digest) != 64 {
		return "", ErrRuntimeControlUnsupported
	}
	content, _, ok := strings.Cut(body, "\nEDO_COMPOSE_"+marker+"\n")
	sum := sha256.Sum256([]byte(content))
	if !ok || len(content) > 128*1024 || hex.EncodeToString(sum[:]) != digest {
		return "", ErrRuntimeControlUnsupported
	}
	return content, nil
}

func (s *Service) recreateComposeService(ctx context.Context, record *model.DeploymentRecord, endpoint string, scope dockerengine.ComposeProjectScope, plan *dockerengine.ComposeContainerControlPlan, actorID string, timeout time.Duration) (dockerengine.RuntimeState, error) {
	if s.ssh == nil {
		return dockerengine.RuntimeState{}, ErrRuntimeControlUnsupported
	}
	content, err := frozenComposeContent(record)
	if err != nil {
		return dockerengine.RuntimeState{}, err
	}
	environment := map[string]string{}
	if record.UsesDeliveryVariables {
		environment, err = s.resolveRecordVariablesAsActor(ctx, record, actorID)
		if err != nil {
			return dockerengine.RuntimeState{}, err
		}
	}
	if environment == nil {
		environment = map[string]string{}
	}
	var run model.PipelineRun
	if err = s.db.WithContext(ctx).First(&run, "uuid = ?", record.PipelineRunID).Error; err != nil {
		return dockerengine.RuntimeState{}, err
	}
	environment["EDO_PIPELINE_RUN_ID"] = run.ID
	environment["EDO_APPLICATION_ID"] = record.ApplicationID
	environment["EDO_GIT_REF"] = run.Ref
	environment["EDO_COMMIT_SHA"] = run.CommitSHA
	environment["EDO_DEPLOYMENT_ID"] = record.ID
	environment["EDO_DEPLOYMENT_TARGET_ID"] = record.TargetID
	var application model.Application
	if err = s.db.WithContext(ctx).Select("name").First(&application, "uuid = ?", record.ApplicationID).Error; err != nil {
		return dockerengine.RuntimeState{}, err
	}
	environment["EDO_APPLICATION_NAME"] = application.Name
	if scope.TagVariable != "" && record.ArtifactID != "" {
		var artifact model.Artifact
		if err = s.db.WithContext(ctx).First(&artifact, "uuid = ?", record.ArtifactID).Error; err != nil {
			return dockerengine.RuntimeState{}, err
		}
		name, parseErr := reference.ParseNormalizedNamed(artifact.Name)
		if parseErr != nil {
			return dockerengine.RuntimeState{}, parseErr
		}
		if tagged, ok := name.(reference.NamedTagged); ok {
			environment[scope.TagVariable] = tagged.Tag()
		}
	}
	script, err := composeRecreateScript(record.ID, scope, plan, content)
	if err != nil {
		return dockerengine.RuntimeState{}, err
	}
	if err = s.requireContainerControl(ctx, record, actorID, "recreate"); err != nil {
		return dockerengine.RuntimeState{}, err
	}
	result, err := s.ssh.RunHostDeploymentScript(ctx, sshdeploy.Input{HostID: record.HostID, EnvironmentID: record.EnvironmentID, WorkingDirectory: record.WorkingDirectory, Script: script, Timeout: timeout, Environment: environment, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.ExitCode != 0 {
		if result.ExitCode == 23 {
			return dockerengine.RuntimeState{}, errors.Join(ErrContainerControlBusy, err)
		}
		if result.ExitCode == 24 {
			return dockerengine.RuntimeState{}, errors.Join(dockerengine.ErrContainerControlChanged, err)
		}
		if result.ExitCode >= 20 && result.ExitCode <= 27 {
			return dockerengine.RuntimeState{}, errors.Join(fmt.Errorf("%w: exit_code=%d", ErrContainerRecreateBaseline, result.ExitCode), err)
		}
		if err == nil {
			return dockerengine.RuntimeState{}, fmt.Errorf("Compose 重建失败 exit_code=%d", result.ExitCode)
		}
		return dockerengine.RuntimeState{}, fmt.Errorf("Compose 重建失败 exit_code=%d: %w", result.ExitCode, err)
	}
	return s.docker.VerifyComposeRecreated(ctx, endpoint, scope, plan)
}

func composeRecreateScript(deploymentID string, scope dockerengine.ComposeProjectScope, plan *dockerengine.ComposeContainerControlPlan, content string) (string, error) {
	if uuid.Validate(deploymentID) != nil {
		return "", ErrRuntimeControlInvalid
	}
	if len(plan.Containers) == 0 || len(plan.Containers) > 256 || plan.Action != "recreate" {
		return "", ErrRuntimeControlInvalid
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	docker := "docker"
	if scope.UseSudo {
		docker = "sudo -n docker"
	}
	args := "--ansi never --project-name " + quote(scope.ProjectName) + " --project-directory " + quote(scope.WorkingDirectory)
	if scope.EnvFile != "" {
		args += " --env-file " + quote(scope.EnvFile)
	}
	file := ".edo/compose/" + scope.ProjectName + "/" + deploymentID + "/compose.yml"
	args += " --file " + quote(file)
	sum := sha256.Sum256([]byte(content + "\n"))
	ids := []string{}
	imageID := plan.Containers[0].ImageID
	for _, container := range plan.Containers {
		if len(container.ID) != 64 || container.ImageID != imageID {
			return "", ErrRuntimeControlUnsupported
		}
		ids = append(ids, container.ID)
	}
	sort.Strings(ids)
	var script strings.Builder
	fmt.Fprintf(&script, "set -eu\numask 077\nstate=%s\nfile_hash() { if command -v sha256sum >/dev/null 2>&1; then sha256sum \"$1\"; elif command -v shasum >/dev/null 2>&1; then shasum -a 256 \"$1\"; else return 1; fi | cut -d ' ' -f 1; }\nfor dir in .edo .edo/compose \"$state\" %s; do [ -d \"$dir\" ] && [ ! -L \"$dir\" ] || exit 20; done\n[ -f %s ] && [ ! -L %s ] || exit 21\n[ \"$(file_hash %s)\" = %s ] || exit 22\nlock=\"$state/active.lock\"\nmkdir \"$lock\" 2>/dev/null || exit 23\ntrap 'rmdir \"$lock\" 2>/dev/null || true' EXIT\ndocker_command() { %s \"$@\"; }\ndc() { docker_command compose %s \"$@\"; }\nservice=%s\nlog=%s\n", quote(".edo/compose/"+scope.ProjectName), quote(".edo/compose/"+scope.ProjectName+"/"+deploymentID), quote(file), quote(file), quote(file), quote(hex.EncodeToString(sum[:])), docker, args, quote(plan.Service), quote(".edo/compose/"+scope.ProjectName+"/"+deploymentID+"/container-recreate.log"))
	script.WriteString("[ ! -L \"$log\" ] && [ ! -d \"$log\" ] || exit 21\n")
	fmt.Fprintf(&script, "actual=$(dc ps --all --quiet \"$service\" 2>>\"$log\" | sort)\n[ \"$actual\" = %s ] || exit 24\nhash=$(dc config --hash \"$service\" 2>>\"$log\") || exit 25\nhash=${hash#* }\n", quote(strings.Join(ids, "\n")))
	for _, container := range plan.Containers {
		fmt.Fprintf(&script, "[ \"$(docker_command inspect --format '{{index .Config.Labels \"com.docker.compose.config-hash\"}}' %s)\" = \"$hash\" ] || exit 26\nimage=$(docker_command inspect --format '{{.Config.Image}}' %s)\n[ \"$(docker_command image inspect --format '{{.Id}}' \"$image\")\" = %s ] || exit 27\n", quote(container.ID), quote(container.ID), quote(container.ImageID))
	}
	fmt.Fprintf(&script, "dc up -d --no-deps --no-build --pull never --force-recreate --scale %s \"$service\" >>\"$log\" 2>&1\n", quote(fmt.Sprintf("%s=%d", plan.Service, len(plan.Containers))))
	return script.String(), nil
}
