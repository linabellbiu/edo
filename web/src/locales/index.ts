import { createI18n } from 'vue-i18n'

export const messages = {
  'zh-CN': {
    common: { search: '搜索', refresh: '刷新', logout: '退出登录', language: '语言', theme: '主题', fullscreen: '全屏' },
    nav: {
      overview: '概览', delivery: '持续交付', applicationsCode: '应用与代码', applications: '应用', repositories: '代码仓库', credentials: '我的令牌',
      buildArtifacts: '构建与制品', buildPlans: '构建方案', registries: '镜像仓库', releaseManagement: '发布管理', deploymentPlans: '部署方案',
      environments: '环境管理', pipelinePlans: '流水线方案', releasePlans: '发布计划', pipelineRuns: '流水线运行', deploymentRecords: '发布记录',
      platform: '平台管理', infrastructure: '基础设施', domains: '域名解析', containers: '容器与集群', operations: '可观测与运维',
      monitor: '系统监控', tasks: '任务中心', logs: '日志', security: '系统与安全', settings: '系统设置', users: '用户管理', roles: '角色与功能', audit: '审计日志',
    },
    dashboard: { title: '分析页', healthy: '运行正常', checking: '正在检查', updated: '最近更新' },
  },
  'en-US': {
    common: { search: 'Search', refresh: 'Refresh', logout: 'Sign out', language: 'Language', theme: 'Theme', fullscreen: 'Fullscreen' },
    nav: {
      overview: 'Overview', delivery: 'Delivery', applicationsCode: 'Apps & Code', applications: 'Applications', repositories: 'Repositories', credentials: 'My Tokens',
      buildArtifacts: 'Build & Artifacts', buildPlans: 'Build Plans', registries: 'Registries', releaseManagement: 'Release Management', deploymentPlans: 'Deployment Plans',
      environments: 'Environments', pipelinePlans: 'Pipeline Templates', releasePlans: 'Release Plans', pipelineRuns: 'Pipeline Runs', deploymentRecords: 'Deployments',
      platform: 'Platform', infrastructure: 'Infrastructure', domains: 'DNS', containers: 'Containers & Clusters', operations: 'Operations',
      monitor: 'System Monitor', tasks: 'Tasks', logs: 'Logs', security: 'System & Security', settings: 'Settings', users: 'Users', roles: 'Roles & Permissions', audit: 'Audit Logs',
    },
    dashboard: { title: 'Analytics', healthy: 'Healthy', checking: 'Checking', updated: 'Updated' },
  },
} as const

export type AppLocale = keyof typeof messages

export default createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackLocale: 'zh-CN',
  messages,
})
