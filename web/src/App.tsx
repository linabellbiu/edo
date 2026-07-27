import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import {
  Navigate,
  NavLink,
  Outlet,
  Route,
  Routes,
  useLocation,
  useNavigate,
} from 'react-router-dom'

import { useAuthStore } from '@/stores/auth'
import LoginView from '@/views/LoginView'
import OverviewView from '@/views/OverviewView'

const DevOpsView = lazy(() => import('@/views/DevOpsView'))
const InfrastructureView = lazy(() => import('@/views/InfrastructureView'))
const ManagementView = lazy(() => import('@/views/ManagementView'))
const CredentialView = lazy(() => import('@/views/CredentialView'))
const AccessView = lazy(() => import('@/views/AccessView'))
const DomainView = lazy(() => import('@/views/DomainView'))
const SettingsView = lazy(() => import('@/views/SettingsView'))
const EnvironmentView = lazy(() => import('@/views/EnvironmentView'))
const PipelinePlanListView = lazy(() => import('@/views/ReleaseWorkflowListView'))
const PipelinePlanView = lazy(() => import('@/views/ReleaseWorkflowView'))

type IconName = 'home' | 'app' | 'git' | 'key' | 'build' | 'registry' | 'release' | 'pipeline' | 'cloud' | 'globe' | 'ops' | 'shield' | 'settings'

const navigationGroups = [
  {
    label: '工作台',
    items: [{ label: '概览', path: '/', permissions: ['system.read'], icon: 'home' as IconName }],
  },
  {
    label: '持续交付',
    items: [
      { label: '应用', path: '/applications', permissions: ['delivery.read'], icon: 'app' as IconName },
      { label: '代码仓库', path: '/repositories', permissions: ['repository.read'], icon: 'git' as IconName },
      { label: '我的令牌', path: '/credentials', permissions: ['credential.read'], icon: 'key' as IconName },
      { label: '构建方案', path: '/build-plans', permissions: ['delivery.read'], icon: 'build' as IconName },
      { label: '镜像仓库', path: '/image-registries', permissions: ['delivery.read'], icon: 'registry' as IconName },
      { label: '部署方案', path: '/deployment-plans', permissions: ['delivery.read'], icon: 'release' as IconName },
	  { label: '环境管理', path: '/environments', permissions: ['deployment.read'], icon: 'cloud' as IconName },
	  { label: '流水线方案', path: '/pipeline-plans', permissions: ['delivery.read'], icon: 'pipeline' as IconName },
      { label: '发布计划', path: '/release-plans', permissions: ['delivery.read', 'deployment.read'], icon: 'release' as IconName },
    ],
  },
  {
    label: '平台管理',
    items: [
      { label: '域名解析', path: '/domains', permissions: ['dns.read'], icon: 'globe' as IconName },
      { label: '容器与集群', path: '/infrastructure', permissions: ['cluster.read', 'terminal.open'], icon: 'cloud' as IconName },
      { label: '运维中心', path: '/operations', permissions: ['task.read', 'monitor.read', 'notification.read', 'scheduler.read', 'config.read'], icon: 'ops' as IconName },
      { label: '系统设置', path: '/settings', permissions: ['config.read', 'identity.read'], icon: 'settings' as IconName },
      { label: '身份与审计', path: '/access', permissions: ['user.read', 'user.manage', 'role.read', 'role.manage', 'audit.read'], icon: 'shield' as IconName },
    ],
  },
]

const pageTitles: Record<string, string> = {
  '/': '概览', '/applications': '应用', '/repositories': '代码仓库', '/build-plans': '构建方案',
  '/credentials': '我的 Git 令牌',
  '/domains': '域名解析',
  '/image-registries': '镜像仓库', '/deployment-plans': '部署方案', '/release-plans': '发布计划',
	'/environments': '环境管理',
	'/pipeline-plans': '流水线方案', '/pipeline-plans/editor': '编辑流水线方案',
  '/infrastructure': '容器与集群', '/operations': '运维中心', '/settings': '系统设置', '/access': '身份与审计', '/identity-providers': '登录方式',
}

interface PageTab {
  key: string
  label: string
  path: string
}

const overviewTab: PageTab = { key: '/', label: '概览', path: '/' }
const redirectPaths = new Set(['/release-workflows', '/release-workflows/editor', '/pipelines', '/deployments', '/identity-providers'])

function loadPageTabs(storageKey: string): PageTab[] {
  try {
    const stored = JSON.parse(sessionStorage.getItem(storageKey) || '[]') as PageTab[]
    const valid = stored.filter((item) => item && typeof item.key === 'string' && item.key.startsWith('/') && typeof item.path === 'string' && item.path.startsWith('/') && typeof item.label === 'string')
    return [overviewTab, ...valid.filter((item, index) => item.key !== '/' && valid.findIndex((candidate) => candidate.key === item.key) === index)]
  } catch {
    return [overviewTab]
  }
}

function savePageTabs(storageKey: string, tabs: PageTab[]) {
  try {
    sessionStorage.setItem(storageKey, JSON.stringify(tabs))
  } catch {
    // 浏览器禁用会话存储时，标签栏仍可在当前页面生命周期内使用。
  }
}

function currentPageTab(pathname: string, search: string): PageTab | null {
  if (redirectPaths.has(pathname)) return null
  return {
    key: pathname,
    label: pageTitles[pathname] || '控制台',
    path: `${pathname}${search}`,
  }
}

function NavIcon({ name }: { name: IconName }) {
  const paths: Record<IconName, React.ReactNode> = {
    home: <><path d="M3 11.5 12 4l9 7.5" /><path d="M5.5 10.5V20h13v-9.5M9 20v-6h6v6" /></>,
    app: <><rect x="4" y="4" width="16" height="16" rx="4" /><path d="M8 9h8M8 13h5M8 17h3" /></>,
    git: <><circle cx="6" cy="5" r="2" /><circle cx="18" cy="7" r="2" /><circle cx="8" cy="19" r="2" /><path d="M6 7v5a7 7 0 0 0 7 7h-3M8 5h3a7 7 0 0 1 7 7V9" /></>,
    key: <><circle cx="8" cy="12" r="4" /><path d="M12 12h9M17 12v3M20 12v2" /></>,
    build: <><path d="m14 6 4-4 4 4-4 4zM2 18l4-4 4 4-4 4z" /><path d="M18 10v2a6 6 0 0 1-6 6h-2M6 14v-2a6 6 0 0 1 6-6h2" /></>,
    registry: <><path d="m4 7 8-4 8 4-8 4zM4 12l8 4 8-4M4 17l8 4 8-4" /></>,
    release: <><path d="M14 4h6v6M20 4l-8 8" /><path d="M18 13v6a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h6" /></>,
    pipeline: <><circle cx="5" cy="12" r="2.5" /><circle cx="19" cy="6" r="2.5" /><circle cx="19" cy="18" r="2.5" /><path d="M7.5 12h3a4 4 0 0 0 4-4 2 2 0 0 1 2-2M7.5 12h3a4 4 0 0 1 4 4 2 2 0 0 0 2 2" /></>,
    cloud: <><path d="M6 18h11a4 4 0 0 0 .7-7.9A6 6 0 0 0 6.3 8.4 4.8 4.8 0 0 0 6 18Z" /></>,
    globe: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18" /></>,
    ops: <><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6 1.7 1.7 0 0 0 10 3v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z" /></>,
    settings: <><circle cx="12" cy="12" r="3" /><path d="M12 3v2M12 19v2M3 12h2M19 12h2M5.6 5.6 7 7M17 17l1.4 1.4M18.4 5.6 17 7M7 17l-1.4 1.4" /><circle cx="12" cy="12" r="7" /></>,
    shield: <><path d="M12 3 4.5 6v5c0 4.8 3 8.2 7.5 10 4.5-1.8 7.5-5.2 7.5-10V6z" /><path d="m9 12 2 2 4-4" /></>,
  }
  return <svg className="nav-icon" viewBox="0 0 24 24" aria-hidden="true">{paths[name]}</svg>
}

function ProtectedRoute() {
  const user = useAuthStore((state) => state.user)
  const loadFailed = useAuthStore((state) => state.loadFailed)
  const location = useLocation()

  if (loadFailed) return <Navigate to="/login?reason=unavailable" replace />
  if (!user) {
    const redirect = encodeURIComponent(`${location.pathname}${location.search}`)
    return <Navigate to={`/login?redirect=${redirect}`} replace />
  }
  return <Outlet />
}

function PipelinePlansRedirect({ editor = false }: { editor?: boolean }) {
  const location = useLocation()
  return <Navigate to={`/pipeline-plans${editor ? '/editor' : ''}${location.search}`} replace />
}

function ReleasePlansRedirect() {
  const location = useLocation()
  return <Navigate to={`/release-plans${location.search}`} replace />
}

function DeploymentRecordsRedirect() {
  return <Navigate to="/release-plans?view=records" replace />
}

function IdentityProvidersRedirect() {
  return <Navigate to="/settings?section=login-methods" replace />
}

function AppShell() {
  const user = useAuthStore((state) => state.user)
  const logout = useAuthStore((state) => state.logout)
  const navigate = useNavigate()
  const location = useLocation()
  const pageTabsStorageKey = `zrt.page-tabs.${user?.id || 'unknown'}`
  const [pageTabs, setPageTabs] = useState<PageTab[]>(() => loadPageTabs(pageTabsStorageKey))
  const activePageTabRef = useRef<HTMLDivElement>(null)
  const allowed = (permissions: string[]) => Boolean(user?.is_superuser || permissions.some((permission) => user?.permissions.includes(permission)))

  useEffect(() => {
    const current = currentPageTab(location.pathname, location.search)
    if (!current) return
    setPageTabs((tabs) => {
      const existing = tabs.findIndex((tab) => tab.key === current.key)
      const next = existing >= 0
        ? tabs.map((tab, index) => index === existing ? current : tab)
        : [...tabs, current]
      const normalized = next.some((tab) => tab.key === '/') ? next : [overviewTab, ...next]
      savePageTabs(pageTabsStorageKey, normalized)
      return normalized
    })
  }, [location.pathname, location.search, pageTabsStorageKey])

  useEffect(() => {
    activePageTabRef.current?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [location.pathname, pageTabs.length])

  async function handleLogout() {
    sessionStorage.removeItem(pageTabsStorageKey)
    await logout()
    navigate('/login', { replace: true })
  }

  function closePageTab(key: string) {
    if (key === '/') return
    const closingIndex = pageTabs.findIndex((tab) => tab.key === key)
    const remaining = pageTabs.filter((tab) => tab.key !== key)
    setPageTabs(remaining)
    savePageTabs(pageTabsStorageKey, remaining)
    if (key !== location.pathname) return
    const target = remaining[Math.max(0, Math.min(closingIndex - 1, remaining.length - 1))] || overviewTab
    navigate(target.path)
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand-block">
          <span className="brand-mark">Z</span>
          <div><strong>ZRT</strong><small>持续交付平台</small></div>
        </div>
        <nav className="navigation" aria-label="主导航">
          {navigationGroups.map((group) => {
            const items = group.items.filter((item) => allowed(item.permissions))
            if (!items.length) return null
            return <div className="nav-group" key={group.label}>
              <span className="nav-group-label">{group.label}</span>
              {items.map((item) => <NavLink key={item.path} to={item.path} end={item.path === '/'} className={({ isActive }) => `nav-item${isActive ? ' active' : ''}`}>
                <NavIcon name={item.icon} /><span>{item.label}</span>
              </NavLink>)}
            </div>
          })}
        </nav>
        <div className="sidebar-footer"><span className="environment-dot" />服务运行中</div>
      </aside>

      <main className={`main-content${location.pathname === '/pipeline-plans/editor' ? ' workflow-main' : ''}`}>
        <header className="topbar">
          <div><p className="eyebrow">ZRT</p><h1>{pageTitles[location.pathname] || '控制台'}</h1></div>
          <button className="operator" type="button" onClick={() => void handleLogout()}>
            <span className="avatar">{(user?.nickname || user?.username || '用').slice(0, 1)}</span>
            <span>{user?.nickname || user?.username || '用户'}</span>
            <span className="logout-label">退出</span>
          </button>
        </header>
        <nav className="page-tab-bar" aria-label="已访问页面">
          <div className="page-tabs" role="tablist">
            {pageTabs.map((tab) => {
              const active = tab.key === location.pathname
              return <div ref={active ? activePageTabRef : undefined} className={`page-tab${active ? ' active' : ''}`} key={tab.key}>
                <button className="page-tab-link" type="button" role="tab" aria-selected={active} title={tab.label} onClick={() => navigate(tab.path)}>{tab.label}</button>
                {tab.key !== '/' && <button className="page-tab-close" type="button" aria-label={`关闭${tab.label}`} title={`关闭${tab.label}`} onClick={() => closePageTab(tab.key)}>×</button>}
              </div>
            })}
          </div>
        </nav>
        <Outlet />
      </main>
    </div>
  )
}

function AppRoutes() {
  const user = useAuthStore((state) => state.user)
  const location = useLocation()

  useEffect(() => {
    document.title = location.pathname === '/login' ? '登录 · ZRT' : `${pageTitles[location.pathname] || '控制台'} · ZRT`
  }, [location.pathname])

  return <Suspense fallback={<div className="loading-panel">正在加载…</div>}><Routes>
    <Route path="/login" element={user ? <Navigate to="/" replace /> : <LoginView />} />
    <Route element={<ProtectedRoute />}>
      <Route element={<AppShell />}>
        <Route index element={<OverviewView />} />
        <Route path="applications" element={<DevOpsView section="applications" />} />
        <Route path="repositories" element={<DevOpsView section="repositories" />} />
        <Route path="credentials" element={<CredentialView />} />
        <Route path="domains" element={<DomainView />} />
        <Route path="build-plans" element={<DevOpsView section="build-plans" />} />
        <Route path="image-registries" element={<DevOpsView section="image-registries" />} />
        <Route path="deployment-plans" element={<DevOpsView section="deployment-plans" />} />
		<Route path="environments" element={<EnvironmentView />} />
		<Route path="pipeline-plans" element={<PipelinePlanListView />} />
		<Route path="pipeline-plans/editor" element={<PipelinePlanView />} />
        <Route path="release-plans" element={<DevOpsView section="release-plans" />} />
		<Route path="release-workflows" element={<PipelinePlansRedirect />} />
		<Route path="release-workflows/editor" element={<PipelinePlansRedirect editor />} />
        <Route path="pipelines" element={<ReleasePlansRedirect />} />
        <Route path="deployments" element={<DeploymentRecordsRedirect />} />
        <Route path="infrastructure" element={<InfrastructureView />} />
        <Route path="operations" element={<ManagementView section="operations" />} />
        <Route path="settings" element={<SettingsView />} />
        <Route path="access" element={<AccessView />} />
        <Route path="identity-providers" element={<IdentityProvidersRedirect />} />
      </Route>
    </Route>
    <Route path="*" element={<Navigate to="/" replace />} />
  </Routes></Suspense>
}

export default function App() {
  const loaded = useAuthStore((state) => state.loaded)
  const ensureLoaded = useAuthStore((state) => state.ensureLoaded)
  useEffect(() => { void ensureLoaded() }, [ensureLoaded])
  if (!loaded) return <div className="loading-screen"><span className="loading-dot" />正在连接 ZRT</div>
  return <AppRoutes />
}
