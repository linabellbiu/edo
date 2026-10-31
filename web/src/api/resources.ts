import axios from 'axios'

import client from '@/api/client'

export type ResourceRecord = Record<string, unknown> & { id?: string }

export async function getResources<T extends ResourceRecord>(
  endpoint: string,
  key: string,
): Promise<T[]> {
  const response = await client.get<Record<string, T[]>>(endpoint)
  return response.data[key] ?? []
}

export async function postResource(
  endpoint: string,
  payload?: unknown,
): Promise<unknown> {
  const response = await client.post(endpoint, payload)
  return response.data
}

export async function createResource(
  endpoint: string,
  payload: unknown,
): Promise<unknown> {
  const response = await client.post(endpoint, payload)
  return response.data
}

export function isMutationAcknowledged(error: unknown): boolean {
  return axios.isAxiosError(error) && error.code === 'EDO_MUTATION_ACKNOWLEDGED'
}

export function apiErrorMessage(error: unknown): string {
  if (axios.isAxiosError(error)) {
    if (error.code === 'EDO_MUTATION_ACKNOWLEDGED') return '请求已受理，请重新查看结果，勿重复提交。'
    if (error.code === 'EDO_AUTHORIZATION_PENDING') return '正在重新核验权限，请稍后重试。'
    if (error.code === 'EDO_AUTHORIZATION_CHANGED') return '授权状态已更新，请重新读取。'
    if (error.code === 'EDO_PROJECT_CHANGED') return '项目已切换，请刷新页面后重试'
    const message = (error.response?.data as { message?: string } | undefined)?.message
    return message || (error.response ? `请求失败（${error.response.status}）` : '无法连接 EDO API')
  }
  return '操作失败，请稍后重试'
}
