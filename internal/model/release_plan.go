package model

import "time"

type ReleasePlanStatus string

const (
	ReleasePlanDraft     ReleasePlanStatus = "draft"
	ReleasePlanActive    ReleasePlanStatus = "active"
	ReleasePlanCompleted ReleasePlanStatus = "completed"
	ReleasePlanCanceled  ReleasePlanStatus = "canceled"
)

// ReleasePlan 表示一次迭代、版本或发布列车。具体应用编排由计划内的发布组描述。
type ReleasePlan struct {
	ID          string            `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name        string            `gorm:"type:varchar(128);not null" json:"name"`
	Version     string            `gorm:"type:varchar(64);not null;uniqueIndex" json:"version"`
	Description string            `gorm:"type:varchar(500);not null;default:''" json:"description"`
	Status      ReleasePlanStatus `gorm:"type:varchar(16);not null;default:'draft';index" json:"status"`
	CreatedBy   string            `gorm:"type:varchar(36);not null;index" json:"created_by"`
	UpdatedBy   string            `gorm:"type:varchar(36);not null;index" json:"updated_by"`
	CreatedAt   time.Time         `gorm:"not null;index" json:"created_at"`
	UpdatedAt   time.Time         `gorm:"not null" json:"updated_at"`
	Groups      []ReleaseGroup    `gorm:"foreignKey:ReleasePlanID" json:"groups,omitempty"`
}

func (ReleasePlan) TableName() string { return "release_plans" }

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
	ID             string      `gorm:"type:varchar(36);primaryKey" json:"id"`
	ReleaseGroupID string      `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_release_group_application,priority:1" json:"release_group_id"`
	ApplicationID  string      `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_release_group_application,priority:2" json:"application_id"`
	SortOrder      int         `gorm:"not null;default:0;index" json:"sort_order"`
	CreatedAt      time.Time   `gorm:"not null" json:"created_at"`
	Application    Application `gorm:"foreignKey:ApplicationID" json:"application"`
}

func (ReleaseGroupApplication) TableName() string { return "release_group_applications" }

type ReleaseGroupDependency struct {
	ReleaseGroupID   string `gorm:"type:varchar(36);primaryKey" json:"release_group_id"`
	DependsOnGroupID string `gorm:"type:varchar(36);primaryKey;index" json:"depends_on_group_id"`
}

func (ReleaseGroupDependency) TableName() string { return "release_group_dependencies" }
