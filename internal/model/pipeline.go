package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"gorm.io/gorm"
)

type BuildPlanKind string

const (
	BuildPlanScript     BuildPlanKind = "script"
	BuildPlanDockerfile BuildPlanKind = "dockerfile"
)

type BuildPlan struct {
	ID             string         `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name           string         `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Kind           BuildPlanKind  `gorm:"type:varchar(16);not null;index" json:"kind"`
	Description    string         `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Script         string         `gorm:"type:text;not null" json:"script,omitempty"`
	DockerfilePath string         `gorm:"type:varchar(512);not null;default:''" json:"dockerfile_path,omitempty"`
	ContextPath    string         `gorm:"type:varchar(512);not null;default:'.'" json:"context_path"`
	ArtifactPath   string         `gorm:"type:varchar(512);not null;default:''" json:"artifact_path,omitempty"`
	TimeoutSeconds int            `gorm:"not null;default:1800" json:"timeout_seconds"`
	IsActive       bool           `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy      string         `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt      time.Time      `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time      `gorm:"not null" json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
}

func (BuildPlan) TableName() string { return "build_plans" }

type RegistryProvider string

const (
	RegistryGeneric   RegistryProvider = "generic"
	RegistryHarbor    RegistryProvider = "harbor"
	RegistryDockerHub RegistryProvider = "docker_hub"
)

type ImageRegistry struct {
	ID                   string           `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name                 string           `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Provider             RegistryProvider `gorm:"type:varchar(24);not null;index" json:"provider"`
	Endpoint             string           `gorm:"type:varchar(1024);not null" json:"endpoint"`
	Namespace            string           `gorm:"type:varchar(255);not null;default:''" json:"namespace"`
	Username             string           `gorm:"type:varchar(255);not null;default:''" json:"username,omitempty"`
	CredentialCiphertext string           `gorm:"type:text;not null" json:"-"`
	AllowInsecureHTTP    bool             `gorm:"not null;default:false" json:"allow_insecure_http"`
	IsActive             bool             `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy            string           `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt            time.Time        `gorm:"not null" json:"created_at"`
	UpdatedAt            time.Time        `gorm:"not null" json:"updated_at"`
}

func (ImageRegistry) TableName() string { return "image_registries" }

type DeploymentPlanKind string

const (
	DeploymentPlanScript  DeploymentPlanKind = "script"
	DeploymentPlanHelm    DeploymentPlanKind = "helm"
	DeploymentPlanCompose DeploymentPlanKind = "compose"
	DeploymentPlanDocker  DeploymentPlanKind = "docker"
)

type DeploymentPlan struct {
	ID                 string             `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name               string             `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Kind               DeploymentPlanKind `gorm:"type:varchar(16);not null;index" json:"kind"`
	DeploymentTargetID string             `gorm:"type:varchar(36);not null;default:'';index" json:"deployment_target_id,omitempty"`
	Description        string             `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Script             string             `gorm:"type:text;not null" json:"script,omitempty"`
	HelmChart          string             `gorm:"type:varchar(512);not null;default:''" json:"helm_chart,omitempty"`
	HelmValues         string             `gorm:"type:text;not null" json:"helm_values,omitempty"`
	ComposeFile        string             `gorm:"type:varchar(512);not null;default:''" json:"compose_file,omitempty"`
	ServiceName        string             `gorm:"type:varchar(255);not null;default:''" json:"service_name,omitempty"`
	TimeoutSeconds     int                `gorm:"not null;default:600" json:"timeout_seconds"`
	IsActive           bool               `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy          string             `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt          time.Time          `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time          `gorm:"not null" json:"updated_at"`
	DeploymentTarget   *DeploymentTarget  `gorm:"foreignKey:DeploymentTargetID;-:migration" json:"deployment_target,omitempty"`
}

func (DeploymentPlan) TableName() string { return "deployment_plans" }

func DeploymentPlanExecutionDigest(kind DeploymentPlanKind, script string, timeoutSeconds int) string {
	payload := string(kind) + "\x00" + strconv.Itoa(timeoutSeconds) + "\x00" + script
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

type ApplicationSyncStatus string

const (
	ApplicationSyncIdle     ApplicationSyncStatus = "idle"
	ApplicationSyncChecking ApplicationSyncStatus = "checking"
	ApplicationSyncSynced   ApplicationSyncStatus = "synced"
	ApplicationSyncChanged  ApplicationSyncStatus = "changed"
	ApplicationSyncFailed   ApplicationSyncStatus = "failed"
)

// ApplicationRepository 保存主仓库的分环境监听基线。历史错误版本产生的额外关联只作兼容数据，不参与应用执行。
type ApplicationRepository struct {
	ID                 string                             `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationID      string                             `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_application_repository,priority:1" json:"application_id"`
	RepositoryID       string                             `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_application_repository,priority:2" json:"repository_id"`
	SortOrder          int                                `gorm:"not null;default:0;index" json:"sort_order"`
	LastObservedRef    string                             `gorm:"type:varchar(512);not null;default:''" json:"last_observed_ref,omitempty"`
	LastObservedCommit string                             `gorm:"type:varchar(64);not null;default:''" json:"last_observed_commit,omitempty"`
	LastCheckedAt      *time.Time                         `json:"last_checked_at,omitempty"`
	CreatedAt          time.Time                          `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time                          `gorm:"not null" json:"updated_at"`
	Repository         GitRepository                      `gorm:"foreignKey:RepositoryID" json:"repository"`
	Observations       []ApplicationRepositoryObservation `gorm:"foreignKey:ApplicationRepositoryID" json:"observations,omitempty"`
}

func (ApplicationRepository) TableName() string { return "application_repositories" }

type ApplicationRepositoryObservation struct {
	ID                      string     `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationRepositoryID string     `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_repository_watch,priority:1" json:"application_repository_id"`
	WatchKey                string     `gorm:"type:varchar(64);not null;default:'';index;uniqueIndex:idx_repository_watch,priority:2" json:"watch_key"`
	SourceNodeID            string     `gorm:"type:varchar(64);not null;default:'';index" json:"source_node_id,omitempty"`
	Event                   string     `gorm:"type:varchar(16);not null;default:'';index" json:"event,omitempty"`
	Environment             string     `gorm:"type:varchar(16);not null;index" json:"environment"`
	Ref                     string     `gorm:"type:varchar(512);not null;default:''" json:"ref,omitempty"`
	CommitSHA               string     `gorm:"type:varchar(64);not null;default:''" json:"commit_sha,omitempty"`
	LastCheckedAt           *time.Time `json:"last_checked_at,omitempty"`
	CreatedAt               time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt               time.Time  `gorm:"not null" json:"updated_at"`
}

func (ApplicationRepositoryObservation) TableName() string {
	return "application_repository_observations"
}

type Application struct {
	ID                     string                   `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name                   string                   `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Description            string                   `gorm:"type:varchar(500);not null;default:''" json:"description"`
	RepositoryID           string                   `gorm:"type:varchar(36);not null;index" json:"repository_id"`
	Branch                 string                   `gorm:"type:varchar(255);not null" json:"branch"`
	PollEnabled            bool                     `gorm:"not null;default:true;index" json:"poll_enabled"`
	PollIntervalSeconds    int                      `gorm:"not null;default:3" json:"poll_interval_seconds"`
	WatchPush              bool                     `gorm:"not null;default:true" json:"watch_push"`
	WatchPullRequest       bool                     `gorm:"not null;default:false" json:"watch_pull_request"`
	WatchTags              bool                     `gorm:"not null;default:false" json:"watch_tags"`
	TagPattern             string                   `gorm:"type:varchar(255);not null;default:''" json:"tag_pattern"`
	BuildPlanID            string                   `gorm:"type:varchar(36);not null;default:'';index" json:"build_plan_id,omitempty"`
	ImageRegistryID        string                   `gorm:"type:varchar(36);not null;default:'';index" json:"image_registry_id,omitempty"`
	DeploymentPlanID       string                   `gorm:"column:release_plan_id;type:varchar(36);not null;default:'';index" json:"deployment_plan_id,omitempty"`
	DeploymentTargetID     string                   `gorm:"type:varchar(36);not null;default:'';index" json:"deployment_target_id,omitempty"`
	WorkflowTemplateID     string                   `gorm:"type:varchar(36);not null;default:'';index" json:"workflow_template_id,omitempty"`
	ReleaseApprovalEnabled bool                     `gorm:"not null;default:true" json:"-"`
	LastObservedRef        string                   `gorm:"type:varchar(512);not null;default:''" json:"last_observed_ref,omitempty"`
	LastObservedCommit     string                   `gorm:"type:varchar(64);not null;default:''" json:"last_observed_commit,omitempty"`
	SyncStatus             ApplicationSyncStatus    `gorm:"type:varchar(16);not null;index" json:"sync_status"`
	SyncMessage            string                   `gorm:"type:varchar(255);not null;default:''" json:"sync_message,omitempty"`
	LastCheckedAt          *time.Time               `json:"last_checked_at,omitempty"`
	IsActive               bool                     `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy              string                   `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt              time.Time                `gorm:"not null" json:"created_at"`
	UpdatedAt              time.Time                `gorm:"not null" json:"updated_at"`
	Repository             GitRepository            `gorm:"foreignKey:RepositoryID" json:"repository,omitempty"`
	BuildPlan              *BuildPlan               `gorm:"foreignKey:BuildPlanID;-:migration" json:"build_plan,omitempty"`
	ImageRegistry          *ImageRegistry           `gorm:"foreignKey:ImageRegistryID;-:migration" json:"image_registry,omitempty"`
	DeploymentPlan         *DeploymentPlan          `gorm:"foreignKey:DeploymentPlanID;-:migration" json:"deployment_plan,omitempty"`
	DeploymentTarget       *DeploymentTarget        `gorm:"foreignKey:DeploymentTargetID;-:migration" json:"deployment_target,omitempty"`
	WorkflowTemplate       *ReleaseWorkflowTemplate `gorm:"foreignKey:WorkflowTemplateID;-:migration" json:"workflow_template,omitempty"`
	Environments           []ApplicationEnvironment `gorm:"foreignKey:ApplicationID" json:"environments,omitempty"`
	Workflow               *ReleaseWorkflow         `gorm:"foreignKey:ApplicationID" json:"workflow,omitempty"`
	Repositories           []ApplicationRepository  `gorm:"foreignKey:ApplicationID" json:"-"`
}

func (Application) TableName() string { return "applications" }

type PipelineRunStatus string

const (
	PipelineRunDetected         PipelineRunStatus = "detected"
	PipelineRunReady            PipelineRunStatus = "ready"
	PipelineRunBlocked          PipelineRunStatus = "blocked"
	PipelineRunAwaitingApproval PipelineRunStatus = "awaiting_approval"
	PipelineRunRunning          PipelineRunStatus = "running"
	PipelineRunSucceeded        PipelineRunStatus = "succeeded"
	PipelineRunFailed           PipelineRunStatus = "failed"
	PipelineRunCanceled         PipelineRunStatus = "canceled"
)

// PipelineRunGraph 是运行列表使用的只读流程快照，只暴露绘制执行路径所需的信息，
// 避免把部署目标、分支规则等编辑配置随运行记录返回给前端。
type PipelineRunGraph struct {
	Nodes []PipelineRunGraphNode `json:"nodes"`
	Edges []WorkflowEdge         `json:"edges"`
}

type PipelineRunGraphNode struct {
	ID          string           `json:"id"`
	Type        WorkflowNodeType `json:"type"`
	Name        string           `json:"name"`
	Position    WorkflowPosition `json:"position"`
	Environment string           `json:"environment,omitempty"`
}

type PipelineRun struct {
	ID                         string                  `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationID              string                  `gorm:"type:varchar(36);not null;index" json:"application_id"`
	ReleasePlanExecutionID     string                  `gorm:"type:varchar(36);not null;default:'';index" json:"release_plan_execution_id,omitempty"`
	ReleasePlanExecutionItemID string                  `gorm:"type:varchar(36);not null;default:'';index" json:"release_plan_execution_item_id,omitempty"`
	Trigger                    string                  `gorm:"type:varchar(24);not null;index" json:"trigger"`
	Ref                        string                  `gorm:"type:varchar(512);not null" json:"ref"`
	CommitSHA                  string                  `gorm:"type:varchar(64);not null" json:"commit_sha"`
	CommitMessage              string                  `gorm:"type:varchar(255);not null;default:''" json:"commit_message,omitempty"`
	Status                     PipelineRunStatus       `gorm:"type:varchar(16);not null;index" json:"status"`
	Stage                      string                  `gorm:"type:varchar(32);not null" json:"stage"`
	Environment                string                  `gorm:"type:varchar(16);not null;default:'';index" json:"environment,omitempty"`
	WorkflowID                 string                  `gorm:"type:varchar(36);not null;default:'';index" json:"workflow_id,omitempty"`
	WorkflowRevision           uint64                  `gorm:"not null;default:0" json:"workflow_revision,omitempty"`
	RetryOfID                  string                  `gorm:"type:varchar(36);not null;default:'';index" json:"retry_of_id,omitempty"`
	CurrentNodeID              string                  `gorm:"type:varchar(64);not null;default:'';index" json:"current_node_id,omitempty"`
	WorkflowSnapshot           string                  `gorm:"type:text;not null" json:"-"`
	ExecutionJobID             string                  `gorm:"type:varchar(36);not null;default:'';index" json:"execution_job_id,omitempty"`
	DeploymentID               string                  `gorm:"type:varchar(36);not null;default:'';index" json:"deployment_id,omitempty"`
	Image                      string                  `gorm:"type:varchar(1024);not null;default:''" json:"image,omitempty"`
	Message                    string                  `gorm:"type:varchar(255);not null;default:''" json:"message,omitempty"`
	CreatedBy                  string                  `gorm:"type:varchar(36);not null;default:'';index" json:"created_by,omitempty"`
	ApprovedBy                 *string                 `gorm:"type:varchar(36);index" json:"approved_by,omitempty"`
	ApprovedAt                 *time.Time              `json:"approved_at,omitempty"`
	ApprovalRequired           bool                    `gorm:"-" json:"approval_required"`
	CurrentNodeName            string                  `gorm:"-" json:"current_node_name,omitempty"`
	ExecutionGraph             *PipelineRunGraph       `gorm:"-" json:"execution_graph,omitempty"`
	CreatedAt                  time.Time               `gorm:"not null;index" json:"created_at"`
	UpdatedAt                  time.Time               `gorm:"not null" json:"updated_at"`
	Application                Application             `gorm:"foreignKey:ApplicationID" json:"application,omitempty"`
	Repositories               []PipelineRunRepository `gorm:"foreignKey:PipelineRunID" json:"repositories"`
}

func (PipelineRun) TableName() string { return "pipeline_runs" }

// PipelineRunLog 是流水线运行的追加式日志。自增 ID 同时作为跨数据库兼容的游标，
// WebSocket 重连时可以从最后一条继续读取，避免重复或遗漏构建输出。
type PipelineRunLog struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;index:idx_pipeline_run_log_cursor,priority:2" json:"id"`
	PipelineRunID string    `gorm:"type:varchar(36);not null;index:idx_pipeline_run_log_cursor,priority:1" json:"pipeline_run_id"`
	Stage         string    `gorm:"type:varchar(32);not null;default:'';index" json:"stage"`
	Level         string    `gorm:"type:varchar(16);not null;default:'info'" json:"level"`
	Message       string    `gorm:"type:text;not null" json:"message"`
	CreatedAt     time.Time `gorm:"not null;index" json:"created_at"`
}

func (PipelineRunLog) TableName() string { return "pipeline_run_logs" }

type PipelineRunRepositoryStatus string

const (
	PipelineRunRepositoryPending   PipelineRunRepositoryStatus = "pending"
	PipelineRunRepositoryReady     PipelineRunRepositoryStatus = "ready"
	PipelineRunRepositorySucceeded PipelineRunRepositoryStatus = "succeeded"
	PipelineRunRepositoryFailed    PipelineRunRepositoryStatus = "failed"
)

// PipelineRunRepository 保存运行创建时的仓库、版本和应用方案快照，后续修改应用配置不会改变历史执行语义。
type PipelineRunRepository struct {
	ID                           string                      `gorm:"type:varchar(36);primaryKey" json:"id"`
	PipelineRunID                string                      `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_run_repository,priority:1" json:"pipeline_run_id"`
	RepositoryID                 string                      `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_run_repository,priority:2" json:"repository_id"`
	SortOrder                    int                         `gorm:"not null;default:0;index" json:"sort_order"`
	Ref                          string                      `gorm:"type:varchar(512);not null;default:''" json:"ref,omitempty"`
	CommitSHA                    string                      `gorm:"type:varchar(64);not null;default:''" json:"commit_sha,omitempty"`
	BuildPlanID                  string                      `gorm:"type:varchar(36);not null;default:'';index" json:"build_plan_id,omitempty"`
	ImageRegistryID              string                      `gorm:"type:varchar(36);not null;default:'';index" json:"image_registry_id,omitempty"`
	DeploymentPlanID             string                      `gorm:"column:release_plan_id;type:varchar(36);not null;default:'';index" json:"deployment_plan_id,omitempty"`
	DeploymentPlanKind           DeploymentPlanKind          `gorm:"type:varchar(16);not null;default:''" json:"deployment_plan_kind,omitempty"`
	DeploymentPlanScript         string                      `gorm:"type:text;not null;default:''" json:"-"`
	DeploymentPlanTimeoutSeconds int                         `gorm:"not null;default:0" json:"deployment_plan_timeout_seconds,omitempty"`
	DeploymentPlanDigest         string                      `gorm:"type:varchar(64);not null;default:''" json:"deployment_plan_digest,omitempty"`
	Status                       PipelineRunRepositoryStatus `gorm:"type:varchar(16);not null;default:'pending';index" json:"status"`
	CreatedAt                    time.Time                   `gorm:"not null" json:"created_at"`
	UpdatedAt                    time.Time                   `gorm:"not null" json:"updated_at"`
	Repository                   GitRepository               `gorm:"foreignKey:RepositoryID" json:"repository"`
	BuildPlan                    *BuildPlan                  `gorm:"foreignKey:BuildPlanID;-:migration" json:"build_plan,omitempty"`
	ImageRegistry                *ImageRegistry              `gorm:"foreignKey:ImageRegistryID;-:migration" json:"image_registry,omitempty"`
	DeploymentPlan               *DeploymentPlan             `gorm:"foreignKey:DeploymentPlanID;-:migration" json:"deployment_plan,omitempty"`
}

func (PipelineRunRepository) TableName() string { return "pipeline_run_repositories" }
