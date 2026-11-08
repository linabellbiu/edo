package configuration

import "context"

const githubLogoSettingKey = "UI_GITHUB_LOGO_ENABLED"

type GitHubLogoSettings struct {
	Enabled bool `json:"enabled"`
	Version int  `json:"version"`
}

// GetGitHubLogoSettings 为所有用户提供相同的全局显示状态，首次安装或升级默认保持显示。
func (s *Service) GetGitHubLogoSettings(ctx context.Context) (GitHubLogoSettings, error) {
	settings, err := s.getSystemBooleanSettings(ctx, githubLogoSettingKey, "GitHub 标识", true)
	return GitHubLogoSettings(settings), err
}

func (s *Service) UpdateGitHubLogoSettings(ctx context.Context, actorID string, enabled bool, expectedVersion int) (GitHubLogoSettings, error) {
	settings, err := s.updateSystemBooleanSettings(ctx, actorID, githubLogoSettingKey, "GitHub 标识", enabled, expectedVersion)
	return GitHubLogoSettings(settings), err
}
