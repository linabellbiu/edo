export interface ResourceSnapshot {
  state: 'pending' | 'ok' | 'stale' | 'unavailable'
  collected_at: string | null
}
