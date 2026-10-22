export interface SystemMetrics {
  collected_at: string
  host: { cpu_percent: number; logical_cpus: number; memory_total_bytes: number; memory_used_bytes: number; memory_available_bytes: number; memory_used_percent: number; load_1: number; load_5: number; load_15: number; uptime_seconds: number }
  process: { cpu_percent: number; rss_bytes: number; vms_bytes: number; uptime_seconds: number }
  runtime: { goroutines: number; gomaxprocs: number; heap_alloc_bytes: number; heap_inuse_bytes: number; stack_inuse_bytes: number; sys_bytes: number; next_gc_bytes: number; gc_cycles: number; gc_pause_total_ms: number; last_gc_at?: string }
  worker: { instances: number; consumer: string; capacity: number; active: number; executed: number; succeeded: number; failed: number; retried: number }
  jobs: { total: number; pending: number; running: number; succeeded: number; failed: number; canceled: number }
  outbox: { pending: number; failed: number }
  queue: { connected: boolean; stored_messages: number; stored_bytes: number; consumers: number; pending_messages: number; ack_pending: number; redelivered: number; waiting_pulls: number; dead_messages: number; dead_bytes: number }
  database: { max_open_connections: number; open_connections: number; in_use: number; idle: number; wait_count: number; wait_duration_ms: number }
  unavailable?: string[]
}

export interface MetricsHistory {
  node_id: string
  from: string
  to: string
  interval_seconds: number
  points: SystemMetrics[]
  latest: SystemMetrics
}
