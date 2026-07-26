import { lazy, Suspense, useEffect } from 'react'
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

const DeploymentView = lazy(() => import('@/views/DeploymentView'))
const DevOpsView = lazy(() => import('@/views/DevOpsView'))
const InfrastructureView = lazy(() => import('@/views/InfrastructureView'))
const ManagementView = lazy(() => import('@/views/ManagementView'))
const IdentityProvidersView = lazy(() => import('@/views/IdentityProvidersView'))
const CredentialView = lazy(() => import('@/views/CredentialView'))
const AccessView = lazy(() => import('@/views/AccessView'))
const DomainView = lazy(() => import('@/views/DomainView'))
const ReleaseWorkflowListView = lazy(() => import('@/views/ReleaseWorkflowListView'))
const ReleaseWorkflowView = lazy(() => import('@/views/ReleaseWorkflowView'))

type IconName = 'home' | 'app' | 'git' | 'key' | 'build' | 'registry' | 'release' | 'pipeline' | 'cloud' | 'globe' | 'ops' | 'shield'

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
	  { label: '发布计划', path: '/release-workflows', permissions: ['delivery.read'], icon: 'pipeline' as IconName },
      { label: '流水线', path: '/pipelines', permissions: ['delivery.read'], icon: 'pipeline' as IconName },
      { label: '发布记录', path: '/deployments', permissions: ['deployment.read'], icon: 'release' as IconName },
    ],
  },
  {
    label: '平台管理',
    items: [
      { label: '域名解析', path: '/domains', permissions: ['dns.read'], icon: 'globe' as IconName },
      { label: '容器与集群', path: '/infrastructure', permissions: ['cluster.read', 'terminal.open'], icon: 'cloud' as IconName },
      { label: '运维中心', path: '/operations', permissions: ['task.read', 'monitor.read', 'notification.read', 'scheduler.read', 'config.read'], icon: 'ops' as IconName },
      { label: '身份与审计', path: '/access', permissions: ['user.read', 'user.manage', 'role.read', 'role.manage', 'audit.read'], icon: 'shield' as IconName },
      { label: '登录方式', path: '/identity-providers', permissions: ['identity.read'], icon: 'shield' as IconName },
    ],
  },
]

const pageTitles: Record<string, string> = {
  '/': '概览', '/applications': '应用', '/repositories': '代码仓库', '/build-plans': '构建方案',
  '/credentials': '我的 Git 令牌',
  '/domains': '域名解析',
  '/image-registries': '镜像仓库', '/deployment-plans': '部署方案', '/pipelines': '流水线',
	'/release-workflows': '发布计划', '/release-workflows/editor': '编辑发布计划',
  '/deployments': '发布记录', '/infrastructure': '容器与集群', '/operations': '运维中心', '/access': '身份与审计', '/identity-providers': '登录方式',
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

function DeploymentPlansRedirect() {
  const location = useLocation()
  return <Navigate to={`/deployment-plans${location.search}`} replace />
}

function AppShell() {
  const user = useAuthStore((state) => state.user)
  const logout = useAuthStore((state) => state.logout)
  const navigate = useNavigate()
  const location = useLocation()
  const allowed = (permissions: string[]) => Boolean(user?.is_superuser || permissions.some((permission) => user?.permissions.includes(permission)))

  async function handleLogout() {
    await logout()
    navigate('/login', { replace: true })
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

      <main className="main-content">
        <header className="topbar">
          <div><p className="eyebrow">ZRT</p><h1>{pageTitles[location.pathname] || '控制台'}</h1></div>
          <button className="operator" type="button" onClick={() => void handleLogout()}>
            <span className="avatar">{(user?.nickname || user?.username || '用').slice(0, 1)}</span>
            <span>{user?.nickname || user?.username || '用户'}</span>
            <span className="logout-label">退出</span>
          </button>
        </header>
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
        <Route path="release-plans" element={<DeploymentPlansRedirect />} />
		<Route path="release-workflows" element={<ReleaseWorkflowListView />} />
		<Route path="release-workflows/editor" element={<ReleaseWorkflowView />} />
        <Route path="pipelines" element={<DevOpsView section="pipelines" />} />
        <Route path="deployments" element={<DeploymentView />} />
        <Route path="infrastructure" element={<InfrastructureView />} />
        <Route path="operations" element={<ManagementView section="operations" />} />
        <Route path="access" element={<AccessView />} />
        <Route path="identity-providers" element={<IdentityProvidersView />} />
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
