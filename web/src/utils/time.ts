function parseTime(value?: string | number | Date) {
  if (value === undefined || value === null || value === '') return null
  const date = value instanceof Date ? value : new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

export function formatDateTime(value?: string | number | Date, locale = 'zh-CN', fallback = '—') {
  const date = parseTime(value)
  if (!date) return fallback
  return date.toLocaleString(locale, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
}

export function formatRelativeTime(value?: string | number | Date, now = Date.now(), locale = 'zh-CN', fallback = '—') {
  const date = parseTime(value)
  if (!date) return fallback

  const deltaSeconds = (date.getTime() - now) / 1000
  const absoluteSeconds = Math.abs(deltaSeconds)
  if (absoluteSeconds < 60) {
    if (locale.toLowerCase().startsWith('zh')) return deltaSeconds <= 0 ? '刚刚' : '不到 1 分钟后'
    return deltaSeconds <= 0 ? 'just now' : 'in less than a minute'
  }

  const units: Array<{ unit: Intl.RelativeTimeFormatUnit; seconds: number }> = [
    { unit: 'year', seconds: 365 * 24 * 60 * 60 },
    { unit: 'month', seconds: 30 * 24 * 60 * 60 },
    { unit: 'day', seconds: 24 * 60 * 60 },
    { unit: 'hour', seconds: 60 * 60 },
    { unit: 'minute', seconds: 60 },
  ]
  const selected = units.find((candidate) => absoluteSeconds >= candidate.seconds) || units.at(-1)!
  const amount = Math.sign(deltaSeconds) * Math.max(1, Math.floor(absoluteSeconds / selected.seconds))
  return new Intl.RelativeTimeFormat(locale, { numeric: 'always' }).format(amount, selected.unit)
}

export function formatElapsedTime(value?: string | number | Date, now = Date.now(), locale = 'zh-CN', fallback = '—') {
  const date = parseTime(value)
  if (!date || !Number.isFinite(now)) return fallback

  // 已发生的事件可能比页面计时值稍新，一分钟内统一展示，避免同步时文案跳动。
  if (now - date.getTime() < 60_000) return locale.toLowerCase().startsWith('zh') ? '1 分钟内' : 'within 1 minute'
  return formatRelativeTime(date, now, locale, fallback)
}
