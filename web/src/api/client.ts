import axios, { type InternalAxiosRequestConfig } from 'axios'

export interface ReadyResponse {
  status: 'ok' | 'failed'
  message?: string
  request_id?: string
  checks: Record<'database' | 'redis' | 'nats', 'ok' | 'failed'>
}

export interface User {
	project_id?: string
  id: string
  username: string
  nickname: string
  department_id?: string
  department_name?: string
  is_superuser: boolean
  last_login_at?: string
  permissions: string[]
}

interface UserResponse {
  user: User
}

export interface LoginProvider {
  id: string
  type: 'ldap' | 'generic_oauth' | 'feishu' | 'dingtalk' | 'email' | 'google' | 'github' | 'gitlab'
  display_name: string
}

const client = axios.create({
  baseURL: '/api/v1',
  timeout: 10_000,
  withCredentials: true,
})

let authorizedProject: string | undefined
const selectedProject = () => typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? ''
const projectChangedMessage = '项目已切换，请刷新页面后重试'
const contextRequest = (config: InternalAxiosRequestConfig) => /^\/?auth(?:\/|$)/.test(config.url ?? '') || config.method === 'get' && config.url === '/projects'

client.interceptors.request.use(config => {
  const project = selectedProject()
  const requestedProject = config.headers.has('X-EDO-Project-ID') ? String(config.headers.get('X-EDO-Project-ID') ?? '') : project
  if (!contextRequest(config) && (requestedProject !== project || authorizedProject !== undefined && project !== authorizedProject)) {
    throw new axios.AxiosError(projectChangedMessage, 'EDO_PROJECT_CHANGED', config)
  }
  // 已打开的编辑器和确认框可以固定项目，不能在发送时被新选择覆盖。
  if (project && !config.headers.has('X-EDO-Project-ID')) config.headers.set('X-EDO-Project-ID', project)
  return config
})

function responseProjectChanged(config?: InternalAxiosRequestConfig) {
  if (!config || contextRequest(config)) return false
  const project = String(config.headers.get('X-EDO-Project-ID') ?? '')
  return project !== selectedProject() || authorizedProject !== undefined && project !== authorizedProject
}

client.interceptors.response.use(response => {
  if (responseProjectChanged(response.config)) throw new axios.AxiosError(projectChangedMessage, 'EDO_PROJECT_CHANGED', response.config)
  return response
}, error => {
  if (axios.isAxiosError(error) && responseProjectChanged(error.config)) throw new axios.AxiosError(projectChangedMessage, 'EDO_PROJECT_CHANGED', error.config)
  if (axios.isAxiosError(error) && error.response?.status === 403 && error.config && !contextRequest(error.config) && typeof window !== 'undefined') {
    // 只通知当前范围重新核验权限；原请求仍失败，尤其不能自动重放写操作。
    window.dispatchEvent(new CustomEvent('edo:permissions-stale', { detail: { project: selectedProject() } }))
  }
  return Promise.reject(error)
})

export async function getReadyStatus(): Promise<ReadyResponse> {
  const response = await client.get<ReadyResponse>('/health/ready', {
    validateStatus: (status) => status === 200 || status === 503,
  })
  return response.data
}

export async function login(username: string, password: string): Promise<User> {
  const response = await client.post<UserResponse>('/auth/login', { username, password })
  return response.data.user
}

export async function loginLDAP(providerID: string, username: string, password: string): Promise<User> {
  const response = await client.post<UserResponse>(`/auth/ldap/${encodeURIComponent(providerID)}/login`, { username, password })
  return response.data.user
}

export async function getLoginProviders(): Promise<LoginProvider[]> {
  const response = await client.get<{ providers: LoginProvider[] }>('/auth/providers')
  return response.data.providers
}

export async function getCurrentUser(): Promise<User> {
  const project = selectedProject()
  const response = await client.get<UserResponse>('/auth/me', { headers: { 'X-EDO-Project-ID': project } })
  if (project !== selectedProject() || (response.data.user.project_id ?? '') !== project) {
    throw new axios.AxiosError(projectChangedMessage, 'EDO_PROJECT_CHANGED', response.config)
  }
  authorizedProject = project
  return response.data.user
}

export async function logout(): Promise<void> {
  try {
    await client.post('/auth/logout')
  } finally {
    authorizedProject = undefined
  }
}

export default client
