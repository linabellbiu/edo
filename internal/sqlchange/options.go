package sqlchange

import (
	"context"

	"edo/internal/model"
)

func (s *Service) Options(ctx context.Context, actorID string) (*OptionsResult, error) {
	if err := s.authorize(ctx, actorID, "sql_change.read"); err != nil {
		return nil, err
	}
	scope, _ := scopeFrom(ctx)
	result := &OptionsResult{Applications: []ApplicationChoice{}, PipelineRuns: []RunChoice{}, Databases: []DatabaseChoice{}, Reviewers: []ReviewerChoice{}}
	var applications []model.Application
	if err := s.db.WithContext(ctx).Where("project_id = ? AND department_id = ? AND is_active = ?", scope.ProjectID, scope.DepartmentID, true).Order("id DESC").Limit(500).Find(&applications).Error; err != nil {
		return nil, s.failure(ctx, "sql_change_options_applications", err, ErrStorage)
	}
	repositoryIDs := make([]string, 0, len(applications))
	for _, app := range applications {
		repositoryIDs = append(repositoryIDs, app.RepositoryID)
	}
	var repositories []model.GitRepository
	if len(repositoryIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("project_id = ? AND uuid IN ?", scope.ProjectID, repositoryIDs).Select("uuid", "name").Find(&repositories).Error; err != nil {
			return nil, s.failure(ctx, "sql_change_options_repositories", err, ErrStorage)
		}
	}
	repositoryNames := map[string]string{}
	for _, repo := range repositories {
		repositoryNames[repo.ID] = repo.Name
	}
	for _, app := range applications {
		result.Applications = append(result.Applications, ApplicationChoice{ID: app.ID, Name: app.Name, RepositoryID: app.RepositoryID, RepositoryName: repositoryNames[app.RepositoryID]})
	}
	var runs []model.PipelineRun
	if err := s.db.WithContext(ctx).Where("project_id = ? AND department_id = ?", scope.ProjectID, scope.DepartmentID).Order("id DESC").Limit(500).Find(&runs).Error; err != nil {
		return nil, s.failure(ctx, "sql_change_options_runs", err, ErrStorage)
	}
	for _, run := range runs {
		result.PipelineRuns = append(result.PipelineRuns, RunChoice{ID: run.ID, ApplicationID: run.ApplicationID, Ref: run.Ref, CommitSHA: run.CommitSHA, Message: run.Message, Status: run.Status, CreatedAt: run.CreatedAt})
	}
	var targets []model.ApplicationDatabase
	if err := s.db.WithContext(ctx).Where("project_id = ? AND department_id = ? AND is_active = ?", scope.ProjectID, scope.DepartmentID, true).Order("id DESC").Limit(500).Find(&targets).Error; err != nil {
		return nil, s.failure(ctx, "sql_change_options_databases", err, ErrStorage)
	}
	var environments []model.Environment
	if err := s.db.WithContext(ctx).Where("project_id = ? AND department_id = ?", scope.ProjectID, scope.DepartmentID).Find(&environments).Error; err != nil {
		return nil, s.failure(ctx, "sql_change_options_environments", err, ErrStorage)
	}
	names := map[string]string{}
	for _, env := range environments {
		names[env.ID] = env.Name
	}
	for _, target := range targets {
		result.Databases = append(result.Databases, DatabaseChoice{ID: target.ID, Name: target.Name, ApplicationID: target.ApplicationID, EnvironmentID: target.EnvironmentID, EnvironmentName: names[target.EnvironmentID], Driver: target.Driver, Host: target.Host, Port: target.Port, Database: target.Database, Version: target.Version})
	}
	var members []model.ProjectMember
	if err := s.db.WithContext(ctx).Where("project_id = ?", scope.ProjectID).Find(&members).Error; err != nil {
		return nil, s.failure(ctx, "sql_change_options_reviewers", err, ErrStorage)
	}
	ids := make([]string, 0, len(members))
	for _, member := range members {
		if member.UserID != actorID {
			ids = append(ids, member.UserID)
		}
	}
	var users []model.User
	if len(ids) > 0 {
		if err := s.db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", ids, true).Order("username ASC").Find(&users).Error; err != nil {
			return nil, s.failure(ctx, "sql_change_options_reviewer_users", err, ErrStorage)
		}
	}
	for _, user := range users {
		if !user.IsSuperuser && user.DepartmentID != scope.DepartmentID {
			continue
		}
		allowed, err := s.permissions.HasPermission(ctx, &user, "sql_change.review")
		if err != nil {
			return nil, s.failure(ctx, "sql_change_options_permission", err, ErrStorage)
		}
		if allowed {
			allowed, err = s.permissions.HasPermission(ctx, &user, "sql_change.read")
			if err != nil {
				return nil, s.failure(ctx, "sql_change_options_reviewer_read", err, ErrStorage)
			}
		}
		if allowed {
			result.Reviewers = append(result.Reviewers, ReviewerChoice{ID: user.ID, Name: user.Username})
		}
	}
	return result, nil
}
