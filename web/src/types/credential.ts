import type { ResourceRecord } from '@/api/resources'

export interface GitCredential extends ResourceRecord {
  id: string
  name: string
  provider: string
  auth_type: 'token' | 'ssh_key'
  username?: string
  secret_hint: string
  updated_at?: string
}
