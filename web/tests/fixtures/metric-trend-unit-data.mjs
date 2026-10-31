// 固定公开合成样本，只用于三天时间标签布局，不证明后台采集或保留期限。
export function createMetricTrendUnitHistory() {
  const from = Date.parse('2026-10-28T00:00:00Z')
  const points = Array.from({ length: 73 }, (_, index) => ({
    collected_at: new Date(from + index * 3600000).toISOString(),
    process: { uptime_seconds: 3600 + index * 3600, rss_bytes: (96 + index % 5) * 1024 ** 2 },
    host: { cpu_percent: 0.6 + index % 5 / 5 },
    jobs: { pending: index % 3 },
  }))
  return { node_id: 'local-visual-samples', from: points[0].collected_at, to: points.at(-1).collected_at, interval_seconds: 3600, points, latest: points.at(-1) }
}
