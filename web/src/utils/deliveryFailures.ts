export interface FailureRun {
  id: string
  created_at: string
  release_plan_id?: string
  release_plan_execution_id?: string
}

export interface FailureBucket {
  label: string
  start: number
  end: number
  pipeline: number
  releasePlan: number
}

export interface FailureSummary {
  buckets: FailureBucket[]
  pipelineTotal: number
  releasePlanTotal: number
}

const HOUR = 3600_000, DAY = 24 * HOUR

/** 失败趋势按窗口自动选择桶宽：15 分钟 / 每小时 / 每天。 */
export function failureBucketSize(windowSeconds: number) {
  if (windowSeconds <= 3600) return 5 * 60_000
  if (windowSeconds <= 6 * 3600) return 30 * 60_000
  if (windowSeconds <= 86400) return HOUR
  if (windowSeconds < 3 * 86400) return 3 * HOUR
  return DAY
}

function bucketLabel(start: number, size: number) {
  const date = new Date(start)
  const time = date.toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' })
  if (size >= DAY) return date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
  if (size >= HOUR) return date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' }) + ' ' + time
  return time
}

/**
 * 将失败运行归入时间桶：发布计划内运行按执行 ID 去重后计入“发布计划失败”，
 * 其余失败运行计入“流水线失败”。只统计窗口内的记录，不做补零或估算。
 */
export function buildFailureBuckets(runs: FailureRun[], windowSeconds: number, now = Date.now()): FailureSummary {
  const size = failureBucketSize(windowSeconds)
  const from = now - windowSeconds * 1000
  const count = Math.max(1, Math.ceil((now - from) / size))
  const buckets: FailureBucket[] = Array.from({ length: count }, (_, index) => {
    const start = from + index * size
    return { label: bucketLabel(start, size), start, end: start + size, pipeline: 0, releasePlan: 0 }
  })
  const seenExecutions = new Set<string>()
  let pipelineTotal = 0, releasePlanTotal = 0
  for (const run of runs) {
    const created = Date.parse(run.created_at)
    if (!Number.isFinite(created) || created < from || created > now) continue
    const bucket = buckets[Math.min(buckets.length - 1, Math.max(0, Math.floor((created - from) / size)))]
    if (run.release_plan_execution_id) {
      const key = run.release_plan_execution_id
      if (seenExecutions.has(key)) continue
      seenExecutions.add(key)
      bucket.releasePlan += 1
      releasePlanTotal += 1
      continue
    }
    bucket.pipeline += 1
    pipelineTotal += 1
  }
  return { buckets, pipelineTotal, releasePlanTotal }
}
