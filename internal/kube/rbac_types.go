package kube

import (
	"errors"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
)

var (
	ErrRBACForbidden   = errors.New("没有当前集群授权范围的管理或访问权限")
	ErrRBACSyncPending = errors.New("集群 RBAC 授权尚未同步完成，请稍后重试或联系管理员")
	ErrRBACConflict    = errors.New("集群授权已被修改，请刷新后重试")
	ErrRBACInvalid     = errors.New("集群授权配置无效，请检查角色、范围和操作权限")
	ErrRBACNotFound    = errors.New("集群授权不存在")
)

type RBACGrantInput struct {
	RoleID           string   `json:"role_id"`
	Namespace        string   `json:"namespace"`
	ClusterScope     bool     `json:"cluster_scope"`
	Permissions      []string `json:"permissions"`
	ExpectedRevision uint64   `json:"expected_revision"`
}

type RBACGrant struct {
	ID           string    `json:"id"`
	RoleID       string    `json:"role_id"`
	RoleName     string    `json:"role_name"`
	Namespace    string    `json:"namespace"`
	ClusterScope bool      `json:"cluster_scope"`
	Permissions  []string  `json:"permissions"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type RBACState struct {
	ClusterID      string      `json:"cluster_id"`
	Enabled        bool        `json:"enabled"`
	DesiredEnabled bool        `json:"desired_enabled"`
	Revision       uint64      `json:"revision"`
	Status         string      `json:"status"`
	LastError      string      `json:"last_error"`
	Grants         []RBACGrant `json:"grants"`
}

type RBACRulePreview struct {
	Subject      string              `json:"subject,omitempty"`
	Namespace    string              `json:"namespace"`
	ClusterScope bool                `json:"cluster_scope"`
	Rules        []rbacv1.PolicyRule `json:"rules"`
}

type RBACPreview struct {
	Grant         RBACGrant         `json:"grant"`
	Rules         []RBACRulePreview `json:"rules"`
	Subjects      []string          `json:"subjects"`
	AffectedUsers int               `json:"affected_users"`
	Warnings      []string          `json:"warnings"`
	Revision      uint64            `json:"revision"`
}
