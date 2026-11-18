package configuration

import "context"

const (
	githubLogoSettingKey = "UI_GITHUB_LOGO_ENABLED"
	// 应用数据库与主机组保护入口默认隐藏，管理员可在界面设置中显式开启。
	deliveryControlsMenuSettingKey = "UI_DELIVERY_CONTROLS_MENU_ENABLED"
)

type GitHubLogoSettings struct {
	Enabled bool `json:"enabled"`
	Version int  `json:"version"`
}

type DeliveryControlsMenuSettings struct {
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

// GetDeliveryControlsMenuSettings 首次安装或升级默认隐藏这两个入口。
func (s *Service) GetDeliveryControlsMenuSettings(ctx context.Context) (DeliveryControlsMenuSettings, error) {
	settings, err := s.getSystemBooleanSettings(ctx, deliveryControlsMenuSettingKey, "应用数据库与主机组保护入口", false)
	return DeliveryControlsMenuSettings(settings), err
}

func (s *Service) UpdateDeliveryControlsMenuSettings(ctx context.Context, actorID string, enabled bool, expectedVersion int) (DeliveryControlsMenuSettings, error) {
	settings, err := s.updateSystemBooleanSettings(ctx, actorID, deliveryControlsMenuSettingKey, "应用数据库与主机组保护入口", enabled, expectedVersion)
	return DeliveryControlsMenuSettings(settings), err
}
