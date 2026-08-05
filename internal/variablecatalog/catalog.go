package variablecatalog

import "strings"

type Kind string

const (
	KindTemplate              Kind = "template"
	KindEnvironment           Kind = "environment"
	KindBuildArgument         Kind = "build_argument"
	KindDeploymentPlaceholder Kind = "deployment_placeholder"
)

type Scope string

const (
	ScopeNotificationTemplate Scope = "notification_template"
	ScopePipelineScript       Scope = "pipeline_script"
	ScopeDockerfileBuild      Scope = "dockerfile_build"
	ScopeDeploymentScript     Scope = "deployment_script"
	ScopeLifecycleScript      Scope = "lifecycle_script"
	ScopeDockerDeployment     Scope = "docker_deployment"
	ScopeComposeDeployment    Scope = "compose_deployment"
)

type Option struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type Definition struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Syntax          string  `json:"syntax"`
	Label           string  `json:"label"`
	Description     string  `json:"description"`
	Kind            Kind    `json:"kind"`
	Category        string  `json:"category"`
	Scopes          []Scope `json:"scopes"`
	Availability    string  `json:"availability"`
	ManagedBySystem bool    `json:"managed_by_system"`
	Sensitive       bool    `json:"sensitive"`

	scriptReserved bool
}

type Catalog struct {
	SchemaVersion int          `json:"schema_version"`
	Kinds         []Option     `json:"kinds"`
	Scopes        []Option     `json:"scopes"`
	Variables     []Definition `json:"variables"`
}

var kindOptions = []Option{
	{Key: string(KindTemplate), Label: "模板变量", Description: "在通知标题或内容中按需引用，发送前替换为本次运行的实际值。"},
	{Key: string(KindEnvironment), Label: "Shell 环境变量", Description: "由 EDO 在相应脚本进程中注入，通过 Shell 环境变量读取。"},
	{Key: string(KindBuildArgument), Label: "Dockerfile 构建参数", Description: "由语言版本配置生成，并作为 Dockerfile Build Arg 传入。"},
	{Key: string(KindDeploymentPlaceholder), Label: "部署占位符", Description: "只在部署配置解析时替换，不会作为应用容器的环境变量注入。"},
}

var scopeOptions = []Option{
	{Key: string(ScopeNotificationTemplate), Label: "通知模板", Description: "流水线任务的通知标题和通知内容。"},
	{Key: string(ScopePipelineScript), Label: "流水线与构建脚本", Description: "流水线 Shell 任务及 Shell 构建方案的隔离运行容器。"},
	{Key: string(ScopeDockerfileBuild), Label: "Dockerfile 构建", Description: "使用 Dockerfile 构建 OCI 镜像时。"},
	{Key: string(ScopeDeploymentScript), Label: "主机部署脚本", Description: "主机脚本部署方案执行时。"},
	{Key: string(ScopeLifecycleScript), Label: "实例生命周期脚本", Description: "Shell 部署实例执行停止或重启脚本时。"},
	{Key: string(ScopeDockerDeployment), Label: "Docker 部署配置", Description: "Docker 容器部署命令解析时。"},
	{Key: string(ScopeComposeDeployment), Label: "Compose 部署配置", Description: "Docker Compose 配置解析时。"},
}

var definitions = []Definition{
	template("application_name", "application.name", "应用名称", "当前流水线所属应用的名称。", "应用", "运行上下文已创建时可用"),
	template("workflow_name", "workflow.name", "流水线名称", "本次运行绑定的流水线名称。", "流水线", "运行绑定流水线后可用"),
	template("task_name", "task.name", "任务名称", "触发当前通知的任务名称。", "任务", "任务开始执行后可用"),
	template("task_status", "task.status", "任务状态", "触发当前通知的任务结果，当前为“成功”或“失败”。", "任务", "任务产生最终结果后可用"),
	template("git_ref", "git.ref", "代码版本", "带类型说明的代码版本，例如 branch: main 或 tag: v1.2.3。", "代码", "本次运行固定代码版本后可用"),
	template("git_commit", "git.commit", "短 Commit", "本次运行固定 Commit 的前 12 位；通知展示使用，不等同于完整 SHA。", "代码", "本次运行固定 Commit 后可用"),
	template("git_message", "git.message", "提交说明", "本次运行固定 Commit 对应的提交说明。", "代码", "仓库提供提交说明时可用"),
	template("run_trigger", "run.trigger", "触发方式", "本次运行的中文触发方式，例如手动执行、分支推送或流水线重试。", "运行", "运行创建后可用"),
	template("run_created_at", "run.created_at", "执行时间", "本次运行的创建时间，按 Asia/Shanghai 格式化为日期和时间。", "运行", "运行创建后可用"),
	template("run_id", "run.id", "运行 ID", "本次流水线运行的唯一标识。", "运行", "运行创建后可用"),
	template("detail", "detail", "执行说明", "当前任务产生的执行结果或失败说明。", "任务", "任务完成并产生说明后可用"),

	environment("ci", "CI", "CI 标记", "隔离脚本运行时固定为 true，可用于启用工具的非交互 CI 模式。", "运行环境", []Scope{ScopePipelineScript}, "隔离脚本容器启动后可用", true),
	environment("home", "HOME", "用户目录", "隔离脚本运行用户的临时 HOME 目录。", "运行环境", []Scope{ScopePipelineScript}, "隔离脚本容器启动后可用", true),
	environment("tmpdir", "TMPDIR", "临时目录", "隔离脚本运行时允许写入的临时目录。", "运行环境", []Scope{ScopePipelineScript}, "隔离脚本容器启动后可用", true),
	environment("pipeline_run_id", "EDO_PIPELINE_RUN_ID", "流水线运行 ID", "本次流水线运行的唯一标识。", "运行", []Scope{ScopePipelineScript, ScopeDeploymentScript}, "流水线运行创建后可用", true),
	environment("application_id", "EDO_APPLICATION_ID", "应用 ID", "本次运行所属应用的唯一标识。", "应用", []Scope{ScopePipelineScript, ScopeDeploymentScript, ScopeLifecycleScript}, "运行绑定应用后可用", true),
	environment("application_name", "EDO_APPLICATION_NAME", "应用名称", "本次部署所属应用的名称。", "应用", []Scope{ScopeDeploymentScript, ScopeLifecycleScript}, "部署或实例生命周期脚本执行时可用", false),
	environment("git_ref", "EDO_GIT_REF", "原始 Git Ref", "本次运行固定的原始 Git Ref，例如 refs/heads/main；不会转换成展示文案。", "代码", []Scope{ScopePipelineScript, ScopeDeploymentScript}, "本次运行固定代码版本后可用", true),
	environment("commit_sha", "EDO_COMMIT_SHA", "完整 Commit SHA", "本次运行固定的完整 Commit SHA。", "代码", []Scope{ScopePipelineScript, ScopeDeploymentScript}, "本次运行固定 Commit 后可用", true),
	environment("target_platform", "EDO_TARGET_PLATFORM", "目标构建平台", "构建方案解析出的目标平台；多平台构建时为逗号分隔的平台列表。", "构建", []Scope{ScopePipelineScript}, "构建方案已解析目标平台时可用", true),
	environment("target_arch", "EDO_TARGET_ARCH", "目标架构", "单一 Linux 目标平台的架构，例如 amd64 或 arm64。", "构建", []Scope{ScopePipelineScript}, "目标为单一 Linux 平台时可用", true),
	environment("goos", "GOOS", "Go 目标系统", "Go 流水线在单一 Linux 目标下固定为 linux。", "语言工具链", []Scope{ScopePipelineScript}, "Go 工具链且目标为单一 Linux 平台时可用", true),
	environment("goarch", "GOARCH", "Go 目标架构", "Go 流水线在单一 Linux 目标下使用的 amd64 或 arm64 架构。", "语言工具链", []Scope{ScopePipelineScript}, "Go 工具链且目标为单一 Linux 平台时可用", true),
	environment("deployment_target_id", "EDO_DEPLOYMENT_TARGET_ID", "部署目标 ID", "当前主机脚本部署目标的唯一标识。", "部署", []Scope{ScopeDeploymentScript}, "部署任务固定目标后可用", false),
	environment("deployment_id", "EDO_DEPLOYMENT_ID", "部署记录 ID", "当前部署记录或实例的唯一标识。", "部署", []Scope{ScopeDeploymentScript, ScopeLifecycleScript}, "部署记录创建后可用", false),
	environment("artifact_path", "EDO_ARTIFACT_PATH", "制品路径", "文件制品传输并校验后，在目标主机上的绝对路径。", "制品", []Scope{ScopeDeploymentScript}, "文件制品在目标主机暂存成功后可用", false),
	environment("artifact_digest", "EDO_ARTIFACT_DIGEST", "制品摘要", "目标主机已校验文件制品的 SHA-256 摘要。", "制品", []Scope{ScopeDeploymentScript}, "文件制品在目标主机校验成功后可用", false),
	environment("lifecycle_action", "EDO_LIFECYCLE_ACTION", "生命周期动作", "当前实例操作，值为 restart 或 stop。", "实例", []Scope{ScopeLifecycleScript}, "执行 Shell 实例停止或重启脚本时可用", false),
	environment("actor_id", "EDO_ACTOR_ID", "操作者 ID", "发起当前实例生命周期操作的用户标识。", "审计", []Scope{ScopeLifecycleScript}, "执行 Shell 实例停止或重启脚本时可用", false),

	buildArgument("go_version", "GO_VERSION", "Go 版本", "Go 模板所选工具链版本，由 EDO 覆盖同名自定义构建参数。", "Go 流水线选择语言版本后可用"),
	buildArgument("node_version", "NODE_VERSION", "Node.js 版本", "Node.js 模板所选工具链版本，由 EDO 覆盖同名自定义构建参数。", "Node.js 流水线选择语言版本后可用"),
	buildArgument("python_version", "PYTHON_VERSION", "Python 版本", "Python 模板所选工具链版本，由 EDO 覆盖同名自定义构建参数。", "Python 流水线选择语言版本后可用"),

	placeholder("image", "EDO_IMAGE", "制品镜像", "本次部署固定的 OCI 镜像引用。Docker 与 Compose 配置只允许在受控位置引用。", []Scope{ScopeDockerDeployment, ScopeComposeDeployment}, "部署任务固定镜像制品后可用"),
	placeholder("container_name", "EDO_CONTAINER_NAME", "容器名称", "EDO 为当前 Docker 单容器部署生成并校验的容器名称。", []Scope{ScopeDockerDeployment}, "Docker 单容器部署创建命令时可用"),
}

func template(id, name, label, description, category, availability string) Definition {
	return Definition{
		ID: "template." + id, Name: name, Syntax: "{{" + name + "}}", Label: label,
		Description: description, Kind: KindTemplate, Category: category,
		Scopes: []Scope{ScopeNotificationTemplate}, Availability: availability, ManagedBySystem: true,
	}
}

func environment(id, name, label, description, category string, scopes []Scope, availability string, scriptReserved bool) Definition {
	return Definition{
		ID: "environment." + id, Name: name, Syntax: "$" + name, Label: label,
		Description: description, Kind: KindEnvironment, Category: category,
		Scopes: scopes, Availability: availability, ManagedBySystem: true, scriptReserved: scriptReserved,
	}
}

func buildArgument(id, name, label, description, availability string) Definition {
	return Definition{
		ID: "build_argument." + id, Name: name, Syntax: "${" + name + "}", Label: label,
		Description: description, Kind: KindBuildArgument, Category: "语言工具链",
		Scopes: []Scope{ScopeDockerfileBuild}, Availability: availability, ManagedBySystem: true,
	}
}

func placeholder(id, name, label, description string, scopes []Scope, availability string) Definition {
	return Definition{
		ID: "deployment_placeholder." + id, Name: name, Syntax: "${" + name + "}", Label: label,
		Description: description, Kind: KindDeploymentPlaceholder, Category: "部署",
		Scopes: scopes, Availability: availability, ManagedBySystem: true,
	}
}

// Snapshot 返回只读目录的副本。目录只描述语义，不读取或返回任何运行时值。
func Snapshot() Catalog {
	result := Catalog{SchemaVersion: 1, Kinds: append([]Option(nil), kindOptions...), Scopes: append([]Option(nil), scopeOptions...)}
	result.Variables = make([]Definition, len(definitions))
	for index, definition := range definitions {
		definition.Scopes = append([]Scope(nil), definition.Scopes...)
		result.Variables[index] = definition
	}
	return result
}

// RenderNotificationTemplate 只替换模板中明确引用且目录已登记的变量；未知占位符保持原样。
func RenderNotificationTemplate(input string, values map[string]string) string {
	result := input
	for _, definition := range definitions {
		if definition.Kind != KindTemplate {
			continue
		}
		if value, exists := values[definition.Name]; exists {
			result = strings.ReplaceAll(result, definition.Syntax, value)
		}
	}
	return result
}

func ReservedScriptEnvironmentNames() map[string]struct{} {
	result := make(map[string]struct{})
	for _, definition := range definitions {
		if definition.Kind == KindEnvironment && definition.scriptReserved {
			result[definition.Name] = struct{}{}
		}
	}
	return result
}
