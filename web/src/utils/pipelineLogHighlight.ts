import Anser from 'anser'

export type PipelineLogTone = 'plain' | 'error' | 'warning' | 'success' | 'command' | 'stderr'
export interface PipelineLogLine { text: string; tone: PipelineLogTone; style?: Record<string, string> }
interface LogInput { message: string; level: string; node_id?: string; stage?: string }
const levels: Record<string, PipelineLogTone> = {error:'error',fatal:'error',critical:'error',panic:'error',warn:'warning',warning:'warning',success:'success',command:'command',stderr:'stderr'}

function structuredTone(text: string, fallback: PipelineLogTone): PipelineLogTone {
  if (fallback !== 'plain' && fallback !== 'stderr') return fallback
  let value = text.trim()
  if (value.startsWith('[') && value.includes('] ')) value = value.slice(value.indexOf('] ') + 2).trim()
  if (!value.startsWith('{') || !value.endsWith('}')) return fallback
  try {
    const data = JSON.parse(value)
    const level = data?.level ?? data?.severity ?? data?.severity_text ?? data?.['log.level'] ?? data?.log?.level
    return typeof level === 'string' ? levels[level.toLowerCase()] || fallback : fallback
  } catch { return fallback }
}

function color(value: string): string | undefined {
  const parts = value?.split(',').map(part => Number(part.trim()))
  if (!parts || parts.length !== 3 || parts.some(part => !Number.isInteger(part) || part < 0 || part > 255)) return undefined
  return `rgb(${parts.join(',')})`
}
function ansiStyle(entry: Anser.AnserJsonEntry): Record<string,string> {
  const style: Record<string,string> = {}
  if (color(entry.fg)) style.color = color(entry.fg)!
  if (color(entry.bg)) style.backgroundColor = color(entry.bg)!
  if (entry.decorations.includes('bold')) style.fontWeight = '600'
  if (entry.decorations.includes('italic')) style.fontStyle = 'italic'
  const decoration = [entry.decorations.includes('underline') ? 'underline' : '', entry.decorations.includes('strikethrough') ? 'line-through' : ''].filter(Boolean)
  if (decoration.length) style.textDecoration = decoration.join(' ')
  return style
}

// ANSI 解析只生成文本和数值颜色，绝不渲染日志中的 HTML、链接或终端控制操作。
class StreamParser {
  private parser = new Anser()
  private previous: Record<string,string> = {}
  parse(message: string, level: string): PipelineLogLine[] {
    const entries = this.parser.ansiToJson(message, {remove_empty:false})
    const fallback = levels[(level || '').toLowerCase()] || 'plain'
    const result: PipelineLogLine[] = []
    for (const [index, entry] of entries.entries()) {
      const style = index === 0 ? this.previous : ansiStyle(entry)
      this.previous = style
      const parts = entry.content.split('\n')
      for (const [position, part] of parts.entries()) {
        const text = part + (position < parts.length - 1 ? '\n' : '')
        if (text) result.push({text,tone:structuredTone(part,fallback),style})
      }
    }
    return result
  }
}

export function pipelineLogLines(message: string, level: string): PipelineLogLine[] {
  return new StreamParser().parse(message,level)
}

// 颜色状态按任务、阶段及输出流隔离；历史与实时数据共用此入口，切换运行会重新解析。
export function highlightPipelineLogs<T extends LogInput>(logs: T[]): Array<T & {lines: PipelineLogLine[]}> {
  const streams = new Map<string,StreamParser>()
  return logs.map(log => {
    const key = JSON.stringify([log.node_id,log.stage,log.level])
    let parser = streams.get(key)
    if (!parser) { parser = new StreamParser(); streams.set(key,parser) }
    return {...log,lines:parser.parse(log.message,log.level)}
  })
}
