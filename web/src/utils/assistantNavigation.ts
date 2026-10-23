import { flatNavigation } from '@/router/navigation'

export type AssistantNavigationPart =
  | { kind: 'text'; text: string }
  | { kind: 'link' | 'unavailable'; labelKey: string; path: string }

// 只识别已有菜单的站内导航，不解析 HTML 或通用 Markdown。
export function assistantNavigationParts(content: string, canAny: (permissions: string[]) => boolean): AssistantNavigationPart[] {
  const targets = new Map(flatNavigation().map(item => [item.path, item]))
  const parts: AssistantNavigationPart[] = []
  const links = /(?<!!)\[([^\]\r\n]{1,100})\]\((\/[^\s()]{1,512})\)/g
  let start = 0
  for (const match of content.matchAll(links)) {
    const item = targets.get(match[2]!)
    if (!item) continue
    if (match.index! > start) parts.push({ kind: 'text', text: content.slice(start, match.index) })
    parts.push({ kind: canAny(item.permissions) ? 'link' : 'unavailable', labelKey: item.label, path: item.path })
    start = match.index! + match[0].length
  }
  if (start < content.length) parts.push({ kind: 'text', text: content.slice(start) })
  return parts
}
