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
const InfrastructureView = lazy(() => import('@/views/InfrastructureView'))
const ManagementView = lazy(() => import('@/views/ManagementView'))

const navigation = [
  { label: '运行概览', path: '/', permissions: ['system.read'] },
  { label: '应用发布', path: '/deployments', permissions: ['deployment.read'] },
  { label: '容器与集群', path: '/infrastructure', permissions: ['cluster.read', 'terminal.open'] },
  { label: '代码仓库', path: '/repositories', permissions: ['repository.read'] },
  { label: '运维中心', path: '/operations', permissions: ['task.read', 'monitor.read', 'notification.read', 'scheduler.read', 'config.read'] },
  { label: '身份与审计', path: '/access', permissions: ['user.read', 'role.read', 'audit.read'] },
]

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

function AppShell() {
  const user = useAuthStore((state) => state.user)
  const logout = useAuthStore((state) => state.logout)
  const navigate = useNavigate()
  const visibleNavigation = navigation.filter((item) => user?.is_superuser || item.permissions.some((permission) => user?.permissions.includes(permission)))

  async function handleLogout() {
    await logout()
    navigate('/login', { replace: true })
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand-block">
          <span className="brand-mark">Z</span>
          <div>
            <strong>ZRT</strong>
            <small>Operations Console</small>
          </div>
        </div>
        <nav className="navigation" aria-label="主导航">
          {visibleNavigation.map((item) => (
            <NavLink
              key={item.label}
              to={item.path}
              end={item.path === '/'}
              className={({ isActive }) => `nav-item${isActive ? ' active' : ''}`}
            >
              <span className="nav-dot" />
              {item.label}
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-footer">
          <span className="environment-dot" />
          容器运维控制台
        </div>
      </aside>

      <main className="main-content">
        <header className="topbar">
          <div>
            <p className="eyebrow">Container operations platform</p>
            <h1>ZRT 控制台</h1>
          </div>
          <button className="operator" type="button" onClick={() => void handleLogout()}>
            {user?.nickname || user?.username || '用户'} · 退出
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
    const titles: Record<string, string> = {
      '/': '运行概览', '/deployments': '应用发布', '/infrastructure': '容器与集群',
      '/repositories': '代码仓库', '/operations': '运维中心', '/access': '身份与审计',
    }
    const title = location.pathname === '/login' ? '登录 · ZRT' : `${titles[location.pathname] || '控制台'} · ZRT`
    document.title = title
  }, [location.pathname])

  return (
    <Suspense fallback={<div className="loading-panel">正在加载模块…</div>}><Routes>
      <Route path="/login" element={user ? <Navigate to="/" replace /> : <LoginView />} />
      <Route element={<ProtectedRoute />}>
        <Route element={<AppShell />}>
          <Route index element={<OverviewView />} />
          <Route path="deployments" element={<DeploymentView />} />
          <Route path="infrastructure" element={<InfrastructureView />} />
          <Route path="repositories" element={<ManagementView section="repositories" />} />
          <Route path="operations" element={<ManagementView section="operations" />} />
          <Route path="access" element={<ManagementView section="access" />} />
        </Route>
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes></Suspense>
  )
}

export default function App() {
  const loaded = useAuthStore((state) => state.loaded)
  const ensureLoaded = useAuthStore((state) => state.ensureLoaded)

  useEffect(() => {
    void ensureLoaded()
  }, [ensureLoaded])

  if (!loaded) return <div className="loading-screen">正在连接 ZRT…</div>
  return <AppRoutes />
}
