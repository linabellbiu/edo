export type PipelineLogTone = 'plain' | 'error' | 'warning' | 'success' | 'command'
export interface PipelineLogLine { text: string; tone: PipelineLogTone }

// 只为原始文本提供视觉提示，不修改日志、任务状态或服务端错误分类。
export function pipelineLogLines(message: string, level: string): PipelineLogLine[] {
  const parts = message.split('\n')
  return parts.map((part, index) => {
    const text = part + (index < parts.length - 1 ? '\n' : '')
    const content = part.replace(/^(?:\[[^\]\r\n]+\]\s*)+/, '').trim()
    const diagnostic = content.replace(/\b(?:no errors?|0 errors?|errors?\s*[:=]\s*0|0 warnings?|warnings?\s*[:=]\s*0)\b|(?:没有|未发现|无)错误/gi, '')
    let tone: PipelineLogTone = 'plain'
    if (level === 'error') tone = 'error'
    else if (level === 'warning' || level === 'warn') tone = 'warning'
    else if (/^执行命令[：:]/.test(content)) tone = 'command'
    else if (/(?:^|\s)\[(?:error|fatal|panic)\]/i.test(part) || /["']?level["']?\s*[:=]\s*["']?(?:error|fatal|panic)\b/i.test(content) ||
      /(?:^|\s)(?:error|fatal|panic)(?:\b|:)|\bfailed (?:to|with)|\bpull access denied\b|\bpermission denied\b|\bconnection refused\b|\b(?:is |became )?unhealthy\b|\bexit code\s*[:=]?\s*[1-9]\d*\b|失败|错误|异常|未通过|不健康|无法|超时/i.test(diagnostic)) tone = 'error'
    else if (/(?:^|\s)\[warn(?:ing)?\]/i.test(part) || /["']?level["']?\s*[:=]\s*["']?warn(?:ing)?\b/i.test(content) || /(?:^|\s)(?:warn(?:ing)?)(?:\b|\[)|警告|已弃用/i.test(diagnostic)) tone = 'warning'
    else if (level === 'success' || /\b(?:succeeded|successfully)\b|部署与镜像验收完成/i.test(content)) tone = 'success'
    return { text, tone }
  })
}
