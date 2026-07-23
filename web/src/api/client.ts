import axios from 'axios'

export interface ReadyResponse {
  status: 'ok' | 'failed'
  message?: string
  request_id?: string
  checks: Record<'database' | 'redis' | 'nats', 'ok' | 'failed'>
}

export interface User {
  id: string
  username: string
  nickname: string
  is_superuser: boolean
  last_login_at?: string
  permissions: string[]
}

interface UserResponse {
  user: User
}

const client = axios.create({
  baseURL: '/api/v1',
  timeout: 10_000,
  withCredentials: true,
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

export async function getCurrentUser(): Promise<User> {
  const response = await client.get<UserResponse>('/auth/me')
  return response.data.user
}

export async function logout(): Promise<void> {
  await client.post('/auth/logout')
}

export default client
