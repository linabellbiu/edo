package model

import "time"

type BuildPlanKind string

const (
	BuildPlanScript     BuildPlanKind = "script"
	BuildPlanDockerfile BuildPlanKind = "dockerfile"
)

type BuildPlan struct {
	ID             string        `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name           string        `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Kind           BuildPlanKind `gorm:"type:varchar(16);not null;index" json:"kind"`
	Description    string        `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Script         string        `gorm:"type:text;not null" json:"script,omitempty"`
	DockerfilePath string        `gorm:"type:varchar(512);not null;default:''" json:"dockerfile_path,omitempty"`
	ContextPath    string        `gorm:"type:varchar(512);not null;default:'.'" json:"context_path"`
	ArtifactPath   string        `gorm:"type:varchar(512);not null;default:''" json:"artifact_path,omitempty"`
	TimeoutSeconds int           `gorm:"not null;default:1800" json:"timeout_seconds"`
	IsActive       bool          `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy      string        `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt      time.Time     `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time     `gorm:"not null" json:"updated_at"`
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

type ReleasePlanKind string

const (
	ReleasePlanScript  ReleasePlanKind = "script"
	ReleasePlanHelm    ReleasePlanKind = "helm"
	ReleasePlanCompose ReleasePlanKind = "compose"
	ReleasePlanDocker  ReleasePlanKind = "docker"
)

type ReleasePlan struct {
	ID             string          `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name           string          `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Kind           ReleasePlanKind `gorm:"type:varchar(16);not null;index" json:"kind"`
	Description    string          `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Script         string          `gorm:"type:text;not null" json:"script,omitempty"`
	HelmChart      string          `gorm:"type:varchar(512);not null;default:''" json:"helm_chart,omitempty"`
	HelmValues     string          `gorm:"type:text;not null" json:"helm_values,omitempty"`
	ComposeFile    string          `gorm:"type:varchar(512);not null;default:''" json:"compose_file,omitempty"`
	ServiceName    string          `gorm:"type:varchar(255);not null;default:''" json:"service_name,omitempty"`
	TimeoutSeconds int             `gorm:"not null;default:600" json:"timeout_seconds"`
	IsActive       bool            `gorm:"not null;default:true;index" json:"is_active"`
	CreatedBy      string          `gorm:"type:varchar(36);not null;index" json:"created_by"`
	CreatedAt      time.Time       `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time       `gorm:"not null" json:"updated_at"`
}

func (ReleasePlan) TableName() string { return "release_plans" }

type ApplicationSyncStatus string

const (
	ApplicationSyncIdle     ApplicationSyncStatus = "idle"
	ApplicationSyncChecking ApplicationSyncStatus = "checking"
	ApplicationSyncSynced   ApplicationSyncStatus = "synced"
	ApplicationSyncChanged  ApplicationSyncStatus = "changed"
	ApplicationSyncFailed   ApplicationSyncStatus = "failed"
)

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
	ApplicationRepositoryID string     `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_repository_environment,priority:1" json:"application_repository_id"`
	Environment             string     `gorm:"type:varchar(16);not null;index;uniqueIndex:idx_repository_environment,priority:2" json:"environment"`
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
	ReleasePlanID          string                   `gorm:"type:varchar(36);not null;default:'';index" json:"release_plan_id,omitempty"`
	DeploymentTargetID     string                   `gorm:"type:varchar(36);not null;default:'';index" json:"deployment_target_id,omitempty"`
	WorkflowTemplateID     string                   `gorm:"type:varchar(36);not null;default:'';index" json:"workflow_template_id,omitempty"`
	ReleaseApprovalEnabled bool                     `gorm:"not null;default:true" json:"release_approval_enabled"`
	RepositoryOrdered      bool                     `gorm:"not null;default:false" json:"repository_ordered"`
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
	ReleasePlan            *ReleasePlan             `gorm:"foreignKey:ReleasePlanID;-:migration" json:"release_plan,omitempty"`
	DeploymentTarget       *DeploymentTarget        `gorm:"foreignKey:DeploymentTargetID;-:migration" json:"deployment_target,omitempty"`
	WorkflowTemplate       *ReleaseWorkflowTemplate `gorm:"foreignKey:WorkflowTemplateID;-:migration" json:"workflow_template,omitempty"`
	Environments           []ApplicationEnvironment `gorm:"foreignKey:ApplicationID" json:"environments,omitempty"`
	Workflow               *ReleaseWorkflow         `gorm:"foreignKey:ApplicationID" json:"workflow,omitempty"`
	Repositories           []ApplicationRepository  `gorm:"foreignKey:ApplicationID" json:"repositories"`
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
	PipelineRunCanceled         PipelineRunStatus = "canceled"
)

type PipelineRun struct {
	ID                string                  `gorm:"type:varchar(36);primaryKey" json:"id"`
	ApplicationID     string                  `gorm:"type:varchar(36);not null;index" json:"application_id"`
	Trigger           string                  `gorm:"type:varchar(24);not null;index" json:"trigger"`
	Ref               string                  `gorm:"type:varchar(512);not null" json:"ref"`
	CommitSHA         string                  `gorm:"type:varchar(64);not null" json:"commit_sha"`
	Status            PipelineRunStatus       `gorm:"type:varchar(16);not null;index" json:"status"`
	Stage             string                  `gorm:"type:varchar(32);not null" json:"stage"`
	Environment       string                  `gorm:"type:varchar(16);not null;default:'';index" json:"environment,omitempty"`
	WorkflowID        string                  `gorm:"type:varchar(36);not null;default:'';index" json:"workflow_id,omitempty"`
	WorkflowRevision  uint64                  `gorm:"not null;default:0" json:"workflow_revision,omitempty"`
	CurrentNodeID     string                  `gorm:"type:varchar(64);not null;default:'';index" json:"current_node_id,omitempty"`
	WorkflowSnapshot  string                  `gorm:"type:text;not null" json:"-"`
	Message           string                  `gorm:"type:varchar(255);not null;default:''" json:"message,omitempty"`
	CreatedBy         string                  `gorm:"type:varchar(36);not null;default:'';index" json:"created_by,omitempty"`
	ApprovedBy        *string                 `gorm:"type:varchar(36);index" json:"approved_by,omitempty"`
	ApprovedAt        *time.Time              `json:"approved_at,omitempty"`
	ApprovalRequired  bool                    `gorm:"-" json:"approval_required"`
	RepositoryOrdered bool                    `gorm:"not null;default:false" json:"repository_ordered"`
	CreatedAt         time.Time               `gorm:"not null;index" json:"created_at"`
	UpdatedAt         time.Time               `gorm:"not null" json:"updated_at"`
	Application       Application             `gorm:"foreignKey:ApplicationID" json:"application,omitempty"`
	Repositories      []PipelineRunRepository `gorm:"foreignKey:PipelineRunID" json:"repositories"`
}

func (PipelineRun) TableName() string { return "pipeline_runs" }

type PipelineRunRepositoryStatus string

const (
	PipelineRunRepositoryPending   PipelineRunRepositoryStatus = "pending"
	PipelineRunRepositoryReady     PipelineRunRepositoryStatus = "ready"
	PipelineRunRepositorySucceeded PipelineRunRepositoryStatus = "succeeded"
)

// PipelineRunRepository 保存发布创建时的仓库、版本和方案快照，后续修改仓库配置不会改变历史发布语义。
type PipelineRunRepository struct {
	ID            string                      `gorm:"type:varchar(36);primaryKey" json:"id"`
	PipelineRunID string                      `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_run_repository,priority:1" json:"pipeline_run_id"`
	RepositoryID  string                      `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_run_repository,priority:2" json:"repository_id"`
	SortOrder     int                         `gorm:"not null;default:0;index" json:"sort_order"`
	Ref           string                      `gorm:"type:varchar(512);not null;default:''" json:"ref,omitempty"`
	CommitSHA     string                      `gorm:"type:varchar(64);not null;default:''" json:"commit_sha,omitempty"`
	BuildPlanID   string                      `gorm:"type:varchar(36);not null;default:'';index" json:"build_plan_id,omitempty"`
	ReleasePlanID string                      `gorm:"type:varchar(36);not null;default:'';index" json:"release_plan_id,omitempty"`
	Status        PipelineRunRepositoryStatus `gorm:"type:varchar(16);not null;default:'pending';index" json:"status"`
	CreatedAt     time.Time                   `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time                   `gorm:"not null" json:"updated_at"`
	Repository    GitRepository               `gorm:"foreignKey:RepositoryID" json:"repository"`
	BuildPlan     *BuildPlan                  `gorm:"foreignKey:BuildPlanID;-:migration" json:"build_plan,omitempty"`
	ReleasePlan   *ReleasePlan                `gorm:"foreignKey:ReleasePlanID;-:migration" json:"release_plan,omitempty"`
}

func (PipelineRunRepository) TableName() string { return "pipeline_run_repositories" }
