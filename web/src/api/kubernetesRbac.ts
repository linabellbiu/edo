import client from '@/api/client'

export interface KubernetesRbacPermission {
  code: string
  name: string
  resource_name?: string
  dangerous?: boolean
  resource_scopes?: Array<'namespace'|'cluster'>
}
export interface KubernetesRbacRole {
  id: string
  name: string
  display_name: string
  permissions: string[]
}
export interface KubernetesRbacGrant {
  id: string
  role_id: string
  role_name?: string
  namespace: string
  cluster_scope: boolean
  permissions: string[]
  status?: string
  last_error?: string
}
export interface KubernetesRbacState {
  cluster_id: string
  enabled: boolean
  desired_enabled?: boolean
  revision: number
  status: string
  last_error?: string
  grants: KubernetesRbacGrant[]
  roles?: KubernetesRbacRole[]
  catalog?: KubernetesRbacPermission[]
  namespaces?: string[]
}
export interface KubernetesRbacGrantInput {
  role_id: string
  namespace: string
  cluster_scope: boolean
  permissions: string[]
  expected_revision: number
}
export interface KubernetesRbacPreview {
  grant: KubernetesRbacGrant
  rules: Array<{namespace: string; cluster_scope: boolean; rules: unknown[]}>
  subjects: string[]
  affected_users: number
  warnings: string[]
  revision: number
}

const endpoint = (cluster: string) => `/kubernetes/clusters/${encodeURIComponent(cluster)}/rbac`
export async function getKubernetesRbac(cluster: string, signal?: AbortSignal) {
  return (await client.get<KubernetesRbacState>(endpoint(cluster), {signal})).data
}
export async function previewKubernetesRbacGrant(cluster: string, input: KubernetesRbacGrantInput, id = '', signal?: AbortSignal) {
  return (await client.post<KubernetesRbacPreview>(`${endpoint(cluster)}/grants/preview`, {...input,...(id?{id}:{})}, {signal})).data
}
export async function saveKubernetesRbacGrant(cluster: string, input: KubernetesRbacGrantInput, id = '', signal?: AbortSignal) {
  const options = {signal}
  return id
    ? (await client.put<KubernetesRbacState>(`${endpoint(cluster)}/grants/${encodeURIComponent(id)}`, input, options)).data
    : (await client.post<KubernetesRbacState>(`${endpoint(cluster)}/grants`, input, options)).data
}
export async function deleteKubernetesRbacGrant(cluster: string, id: string, revision: number, signal?: AbortSignal) {
  return (await client.delete<KubernetesRbacState>(`${endpoint(cluster)}/grants/${encodeURIComponent(id)}`, {data:{expected_revision:revision},signal})).data
}
export async function setKubernetesRbacMode(cluster: string, enabled: boolean, revision: number, signal?: AbortSignal) {
  return (await client.put<KubernetesRbacState>(`${endpoint(cluster)}/mode`, {enabled,expected_revision:revision}, {signal})).data
}
export async function syncKubernetesRbac(cluster: string, revision: number, signal?: AbortSignal) {
  return (await client.post<KubernetesRbacState>(`${endpoint(cluster)}/sync`, {expected_revision:revision}, {signal,timeout:150_000})).data
}
