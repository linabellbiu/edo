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

export function apiErrorMessage(error: unknown): string {
  if (axios.isAxiosError(error)) {
    const message = (error.response?.data as { message?: string } | undefined)?.message
    return message || (error.response ? `请求失败（${error.response.status}）` : '无法连接 EDO API')
  }
  return '操作失败，请稍后重试'
}
