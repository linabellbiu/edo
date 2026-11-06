export interface ComposeContainerPort { container_port: string; host_ip?: string; host_port?: string }
export interface ComposeContainerState {
  id: string; name: string; image: string; image_display?: string; image_id: string
  state: string; running: boolean; health?: string
  created_at?: string; started_at?: string; finished_at?: string
  restart_count?: number; exit_code?: number; oom_killed?: boolean; restart_policy?: string
  terminal_unavailable_reason?: string
  ports?: ComposeContainerPort[]; networks?: Array<{ name: string; ip_address?: string }>
}
export interface ComposeServiceState {
  name: string; state: string; count: number; ready_count: number; run_to_completion?: boolean
  containers?: ComposeContainerState[]
}
interface StatusMeta { tone: 'neutral' | 'info' | 'success' | 'warning' | 'danger'; label: string; live: boolean }

export function composeRuntimeStatus(state: string, running = false): StatusMeta {
  if (state === 'completed') return { tone: 'success', label: '已完成', live: false }
  if (running) return { tone: 'success', label: '运行中', live: true }
  const states: Record<string, StatusMeta> = {
    failed: { tone: 'danger', label: '任务失败', live: false }, completing: { tone: 'warning', label: '执行中', live: true },
    partial: { tone: 'warning', label: '部分服务异常', live: false }, unhealthy: { tone: 'danger', label: '健康检查失败', live: false },
    starting: { tone: 'warning', label: '启动中', live: true }, restarting: { tone: 'warning', label: '重启中', live: true },
    missing: { tone: 'warning', label: '服务不存在', live: false }, paused: { tone: 'neutral', label: '已暂停', live: false },
    exited: { tone: 'neutral', label: '已停止', live: false }, created: { tone: 'neutral', label: '尚未启动', live: false },
    dead: { tone: 'danger', label: '异常退出', live: false },
  }
  return states[state] || { tone: 'neutral', label: '状态未知', live: false }
}
export function composeRuntimeColor(state: string, running = state === 'running') {
  return ({ success: 'green', warning: 'gold', danger: 'red', info: 'blue' } as Record<string, string>)[composeRuntimeStatus(state, running).tone]
}
export function composeContainerCanTerminal(container: ComposeContainerState) {
  return container.running === true && !container.terminal_unavailable_reason && ['running', 'unhealthy', 'starting', 'completing'].includes(container.state)
}
export function composePortLabel(port: ComposeContainerPort) {
  if (!port.host_port) return port.container_port
  const host = port.host_ip?.includes(':') ? `[${port.host_ip}]` : port.host_ip
  return `${host ? `${host}:` : ''}${port.host_port} → ${port.container_port}`
}
