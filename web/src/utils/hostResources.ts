export function formatHostCapacity(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value <= 0) return '—'
  const gib = value / 1024 ** 3
  const amount = gib >= 1 ? gib : value / 1024 ** 2
  const digits = amount < 0.01 ? 4 : amount < 1 ? 2 : amount < 10 ? 1 : 0
  const formatted = amount.toLocaleString('zh-CN', { maximumFractionDigits: digits })
  return `${formatted === '0' ? '< 0.0001' : formatted} ${gib >= 1 ? 'GiB' : 'MiB'}`
}

export function formatHostCPU(value: number | null | undefined): string {
  return value != null && Number.isInteger(value) && value > 0 ? `${value} 核` : '—'
}
