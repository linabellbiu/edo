package model

import (
	"time"

	"gorm.io/gorm"
)

type ReleasePlanStatus string

const (
	ReleasePlanDraft     ReleasePlanStatus = "draft"
	ReleasePlanActive    ReleasePlanStatus = "active"
	ReleasePlanCompleted ReleasePlanStatus = "completed"
	ReleasePlanCanceled  ReleasePlanStatus = "canceled"
)

// ReleasePlan 表示一次人工组织的批量发布。Name 和 Version 是服务端生成的内部唯一标识；
// 用户填写 Description，并通过 Groups 编排应用，计划本身不直接关联应用。
type ReleasePlan struct {
	ID              string                `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name            string                `gorm:"type:varchar(128);not null" json:"name"`
	Version         string                `gorm:"type:varchar(64);not null;uniqueIndex:ux_release_plans_department_version,priority:2" json:"version"`
	Description     string                `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Status          ReleasePlanStatus     `gorm:"type:varchar(16);not null;default:'draft';index" json:"status"`
	IsActive        bool                  `gorm:"not null;default:true;index" json:"is_active"`
	DepartmentID    string                `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index;uniqueIndex:ux_release_plans_department_version,priority:1" json:"department_id"`
	CreatedBy       string                `gorm:"type:varchar(36);not null;index" json:"created_by"`
	UpdatedBy       string                `gorm:"type:varchar(36);not null;index" json:"updated_by"`
	CreatedAt       time.Time             `gorm:"not null;index" json:"created_at"`
	UpdatedAt       time.Time             `gorm:"not null" json:"updated_at"`
	DeletedAt       gorm.DeletedAt        `gorm:"index" json:"-"`
	Groups          []ReleaseGroup        `gorm:"foreignKey:ReleasePlanID" json:"groups,omitempty"`
	LatestExecution *ReleasePlanExecution `gorm:"-" json:"latest_execution,omitempty"`
}

func (ReleasePlan) TableName() string { return "release_plans" }

type ReleasePlanExecutionStatus string

const (
	ReleasePlanExecutionPending   ReleasePlanExecutionStatus = "pending"
	ReleasePlanExecutionRunning   ReleasePlanExecutionStatus = "running"
	ReleasePlanExecutionSucceeded ReleasePlanExecutionStatus = "succeeded"
	ReleasePlanExecutionFailed    ReleasePlanExecutionStatus = "failed"
)

// ReleasePlanExecution 固化一次发布计划的编排结构。发布计划只允许执行一次，
// RequestID 用于识别客户端重试，Snapshot 保证后续调和不受计划编辑影响。
type ReleasePlanExecution struct {
	ID            string                     `gorm:"type:varchar(36);primaryKey" json:"id"`
	ReleasePlanID string                     `gorm:"type:varchar(36);not null;uniqueIndex:idx_release_plan_execution_plan;uniqueIndex:idx_release_plan_execution_request,priority:1" json:"release_plan_id"`
	RequestID     string                     `gorm:"type:varchar(128);not null;uniqueIndex:idx_release_plan_execution_request,priority:2" json:"request_id"`
	Status        ReleasePlanExecutionStatus `gorm:"type:varchar(16);not null;default:'pending';index" json:"status"`
	Snapshot      string                     `gorm:"type:text;not null" json:"-"`
	DepartmentID  string                     `gorm:"type:varchar(36);not null;default:'00000000-0000-0000-0000-000000000001';index" json:"department_id"`
	CreatedBy     string                     `gorm:"type:varchar(36);not null;index" json:"created_by"`
	StartedAt     *time.Time                 `json:"started_at,omitempty"`
	FinishedAt    *time.Time                 `json:"finished_at,omitempty"`
	CreatedAt     time.Time                  `gorm:"not null;index" json:"created_at"`
	UpdatedAt     time.Time                  `gorm:"not null" json:"updated_at"`
	Items         []ReleasePlanExecutionItem `gorm:"foreignKey:ReleasePlanExecutionID" json:"items,omitempty"`
}

func (ReleasePlanExecution) TableName() string { return "release_plan_executions" }

type ReleasePlanExecutionItemStatus string

const (
	ReleasePlanExecutionItemPending   ReleasePlanExecutionItemStatus = "pending"
	ReleasePlanExecutionItemRunning   ReleasePlanExecutionItemStatus = "running"
	ReleasePlanExecutionItemSucceeded ReleasePlanExecutionItemStatus = "succeeded"
	ReleasePlanExecutionItemFailed    ReleasePlanExecutionItemStatus = "failed"
	ReleasePlanExecutionItemSkipped   ReleasePlanExecutionItemStatus = "skipped"
	ReleasePlanExecutionItemCanceled  ReleasePlanExecutionItemStatus = "canceled"
)

// ReleasePlanExecutionItem 是发布计划中单个应用的一次不可变执行槽。
// PipelineRun 在创建执行时预先生成，调和器只负责按快照决定何时启动它。
type ReleasePlanExecutionItem struct {
	ID                        string                         `gorm:"type:varchar(36);primaryKey" json:"id"`
	ReleasePlanExecutionID    string                         `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_release_plan_execution_item,priority:1" json:"release_plan_execution_id"`
	ReleaseGroupID            string                         `gorm:"type:varchar(36);not null;index" json:"release_group_id"`
	ReleaseGroupApplicationID string                         `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_release_plan_execution_item,priority:2" json:"release_group_application_id"`
	ApplicationID             string                         `gorm:"type:varchar(36);not null;index" json:"application_id"`
	WorkflowID                string                         `gorm:"type:varchar(36);not null;index" json:"workflow_id"`
	PipelineRunID             string                         `gorm:"type:varchar(36);not null;uniqueIndex" json:"pipeline_run_id"`
	Status                    ReleasePlanExecutionItemStatus `gorm:"type:varchar(16);not null;default:'pending';index" json:"status"`
	Ref                       string                         `gorm:"type:varchar(512);not null" json:"ref"`
	CommitSHA                 string                         `gorm:"type:varchar(64);not null" json:"commit_sha"`
	SourceNodeID              string                         `gorm:"type:varchar(64);not null;index" json:"source_node_id"`
	SortOrder                 int                            `gorm:"not null;default:0;index" json:"sort_order"`
	Message                   string                         `gorm:"type:varchar(255);not null;default:''" json:"message,omitempty"`
	StartedAt                 *time.Time                     `json:"started_at,omitempty"`
	FinishedAt                *time.Time                     `json:"finished_at,omitempty"`
	CreatedAt                 time.Time                      `gorm:"not null" json:"created_at"`
	UpdatedAt                 time.Time                      `gorm:"not null" json:"updated_at"`
}

func (ReleasePlanExecutionItem) TableName() string { return "release_plan_execution_items" }

type ReleaseGroupMode string

const (
	ReleaseGroupParallel   ReleaseGroupMode = "parallel"
	ReleaseGroupSequential ReleaseGroupMode = "sequential"
)

type ReleaseGroupFailurePolicy string

const (
	ReleaseGroupStopOnFailure ReleaseGroupFailurePolicy = "stop"
	ReleaseGroupContinue      ReleaseGroupFailurePolicy = "continue"
)

type ReleaseApplicationSourceType string

const (
	ReleaseApplicationSourceBranch ReleaseApplicationSourceType = "branch"
	ReleaseApplicationSourceCommit ReleaseApplicationSourceType = "commit"
)

// ReleaseGroup 编排一组应用。同组应用按 Mode 并行或串行执行，组间依赖单独保存。
type ReleaseGroup struct {
	ID            string                    `gorm:"type:varchar(36);primaryKey" json:"id"`
	ReleasePlanID string                    `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_release_group_name,priority:1" json:"release_plan_id"`
	Name          string                    `gorm:"type:varchar(128);not null;uniqueIndex:idx_release_group_name,priority:2" json:"name"`
	Mode          ReleaseGroupMode          `gorm:"type:varchar(16);not null;default:'parallel'" json:"mode"`
	FailurePolicy ReleaseGroupFailurePolicy `gorm:"type:varchar(16);not null;default:'stop'" json:"failure_policy"`
	SortOrder     int                       `gorm:"not null;default:0;index" json:"sort_order"`
	CreatedAt     time.Time                 `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time                 `gorm:"not null" json:"updated_at"`
	Applications  []ReleaseGroupApplication `gorm:"foreignKey:ReleaseGroupID" json:"applications,omitempty"`
	Dependencies  []ReleaseGroupDependency  `gorm:"foreignKey:ReleaseGroupID" json:"dependencies,omitempty"`
}

func (ReleaseGroup) TableName() string { return "release_groups" }

type ReleaseGroupApplication struct {
	ID             string                       `gorm:"type:varchar(36);primaryKey" json:"id"`
	ReleaseGroupID string                       `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_release_group_application,priority:1" json:"release_group_id"`
	ApplicationID  string                       `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_release_group_application,priority:2" json:"application_id"`
	ManualDeploy   bool                         `gorm:"not null;default:false" json:"manual_deploy"`
	SourceType     ReleaseApplicationSourceType `gorm:"type:varchar(16);not null;default:''" json:"source_type,omitempty"`
	SourceValue    string                       `gorm:"type:varchar(255);not null;default:''" json:"source_value,omitempty"`
	SortOrder      int                          `gorm:"not null;default:0;index" json:"sort_order"`
	CreatedAt      time.Time                    `gorm:"not null" json:"created_at"`
	Application    Application                  `gorm:"foreignKey:ApplicationID" json:"application"`
}

func (ReleaseGroupApplication) TableName() string { return "release_group_applications" }

type ReleaseGroupDependency struct {
	ReleaseGroupID   string `gorm:"type:varchar(36);primaryKey" json:"release_group_id"`
	DependsOnGroupID string `gorm:"type:varchar(36);primaryKey;index" json:"depends_on_group_id"`
}

func (ReleaseGroupDependency) TableName() string { return "release_group_dependencies" }
