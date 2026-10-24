package sqlchange

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"edo/internal/model"
	"github.com/bsm/redislock"
)

const MaxSQLBytes = 16 * 1024

// Base64 和加密封装后仍需适配三种数据库共同支持的 TEXT 容量。
const maxCheckReportBytes = 32 * 1024

var (
	ErrScope              = errors.New("请先选择有权访问的项目")
	ErrForbidden          = errors.New("没有权限执行此 SQL 工单操作")
	ErrNotFound           = errors.New("SQL 工单或关联资源不存在")
	ErrInvalid            = errors.New("SQL 工单参数无效，请检查发布、目标、审核人和 SQL 内容")
	ErrConflict           = errors.New("SQL 工单已被修改，请刷新后重试")
	ErrImmutable          = errors.New("SQL 工单已执行成功，不能修改或删除")
	ErrState              = errors.New("SQL 工单状态不允许此操作，已执行或跳过的变更不能重复执行")
	ErrUnavailable        = errors.New("SQL 工单执行服务暂时不可用")
	ErrStorage            = errors.New("SQL 工单暂时无法保存或读取，请稍后重试")
	ErrCheckFailed        = errors.New("SQL 静态检查未通过，请查看检查结果")
	ErrExecutionFailed    = errors.New("SQL 执行失败，请核对目标数据库，不会自动重试")
	ErrExecutionUncertain = errors.New("SQL 执行结果不确定，请人工核对目标数据库，不会自动重试")
)

type PermissionChecker interface {
	HasPermission(context.Context, *model.User, string) (bool, error)
}
type LockManager interface {
	Obtain(context.Context, string, time.Duration, *redislock.Options) (*redislock.Lock, error)
}
type CheckResult struct {
	Passed   bool
	Summary  string
	Findings json.RawMessage
}
type Runner interface {
	Check(context.Context, string, string) (CheckResult, error)
	Execute(context.Context, model.ApplicationDatabase, string, string, string) error
}

type Input struct {
	Name            string `json:"name"`
	ApplicationID   string `json:"application_id"`
	RepositoryID    string `json:"repository_id"`
	Branch          string `json:"branch"`
	PipelineRunID   string `json:"pipeline_run_id"`
	DatabaseID      string `json:"database_id"`
	EnvironmentID   string `json:"environment_id"`
	Phase           string `json:"phase"`
	ReviewerID      string `json:"reviewer_id"`
	SQL             string `json:"sql"`
	ExpectedVersion uint64 `json:"expected_version"`
}
type ActionInput struct {
	ExpectedVersion uint64 `json:"expected_version"`
	Decision        string `json:"decision"`
	Reason          string `json:"reason"`
	ConfirmDigest   string `json:"confirm_digest"`
}
type ListInput struct {
	Search, Status, ApplicationID, PipelineRunID string
	Page, PageSize                               int
}
type View struct {
	model.SQLChange
	Digest   string `json:"digest"`
	AuthorID string `json:"author_id"`
}
type ListResult struct {
	Items []View `json:"items"`
	Total int64  `json:"total"`
}
type RevisionView struct {
	model.SQLChangeRevision
	CheckReport json.RawMessage `json:"check_report"`
}
type DetailResult struct {
	Change     View                       `json:"change"`
	SQL        string                     `json:"sql"`
	Revision   RevisionView               `json:"revision"`
	Reviews    []model.SQLChangeReview    `json:"reviews"`
	Executions []model.SQLChangeExecution `json:"executions"`
}
type ApplicationChoice struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
}
type RunChoice struct {
	ID            string                  `json:"id"`
	ApplicationID string                  `json:"application_id"`
	Ref           string                  `json:"ref"`
	CommitSHA     string                  `json:"commit_sha"`
	Message       string                  `json:"message"`
	Status        model.PipelineRunStatus `json:"status"`
	CreatedAt     time.Time               `json:"created_at"`
}
type DatabaseChoice struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ApplicationID   string `json:"application_id"`
	EnvironmentID   string `json:"environment_id"`
	EnvironmentName string `json:"environment_name"`
	Driver          string `json:"driver"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Database        string `json:"database"`
	Version         uint64 `json:"version"`
}
type ReviewerChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type OptionsResult struct {
	Applications []ApplicationChoice `json:"applications"`
	PipelineRuns []RunChoice         `json:"pipeline_runs"`
	Databases    []DatabaseChoice    `json:"databases"`
	Reviewers    []ReviewerChoice    `json:"reviewers"`
}

type taskPayload struct {
	ChangeID    string `json:"change_id"`
	RevisionID  string `json:"revision_id"`
	ExecutionID string `json:"execution_id,omitempty"`
}
