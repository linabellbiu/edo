import type { PipelineStageTiming } from '@/types/pipeline'

export function stageDurationLabel(
  timing?: PipelineStageTiming,
  elapsed = 0,
): string {
  const states: Record<string, string> = {
    pending: '未开始',
    not_executed: '未执行',
    skipped: '已跳过',
    unavailable: '耗时未知',
    queued: '排队中',
    running: '进行中',
    blocked: '等待放行',
    awaiting_approval: '等待审核',
    failed: '失败',
    canceled: '已停止',
  }
  if (!timing) return '耗时未知'
  if (
    ['pending', 'not_executed', 'skipped', 'unavailable'].includes(timing.state)
  )
    return states[timing.state]!
  if (
    timing.duration_ms == null ||
    !Number.isFinite(timing.duration_ms) ||
    timing.duration_ms < 0
  )
    return '耗时未知'
  const duration = formatPipelineDuration(timing.duration_ms + (timing.running ? Math.max(0, elapsed) : 0))
  const state = timing.running
    ? states[timing.state] || '进行中'
    : ['failed', 'canceled'].includes(timing.state)
      ? states[timing.state]
      : ''
  return state ? `${duration} · ${state}` : duration
}

export function formatPipelineDuration(milliseconds?: number | null): string {
  if (milliseconds == null || !Number.isFinite(milliseconds) || milliseconds < 0) return '耗时未知'
  if (milliseconds < 1000) return `${Math.floor(milliseconds)} 毫秒`
  const seconds = Math.floor(milliseconds / 1000)
  return [
    seconds >= 3600 ? `${Math.floor(seconds / 3600)} 小时` : '',
    seconds >= 60 ? `${Math.floor((seconds % 3600) / 60)} 分` : '',
    `${seconds % 60} 秒`,
  ].filter(Boolean).join(' ')
}
