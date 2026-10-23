import client from '@/api/client'

export interface AssistantSettings {
  enabled: boolean
  base_url: string
  model: string
  has_api_key: boolean
  version: number
  max_steps: number
  timeout_seconds: number
}

export interface AssistantSettingsInput {
  enabled: boolean
  base_url: string
  model: string
  max_steps: number
  timeout_seconds: number
  api_key?: string
  clear_api_key?: boolean
  expected_version: number
}

export interface AssistantStatus { enabled: boolean; configured: boolean; reason?: string }
export interface AssistantConversation { id: string; title: string; updated_at?: string; created_at?: string }
export interface AssistantMessage { id: string; role: string; content: string; status?: string; created_at?: string }
export interface AssistantAction {
  id: string
  kind: string
  title: string
  summary: string
  status: string
  target_name: string
  expires_at: string
  result?: { pipeline_run_id?: string; message?: string }
}
export interface AssistantDetail { conversation: AssistantConversation; messages: AssistantMessage[]; actions: AssistantAction[] }
export interface AssistantEvent {
  type: 'start' | 'delta' | 'tool_start' | 'tool_end' | 'action_proposed' | 'done' | 'error'
  text?: string
  tool_name?: string
  action?: AssistantAction
  message?: AssistantMessage
  error?: string
}

export class AssistantStreamError extends Error {
  constructor(message: string) { super(message); this.name = 'AssistantStreamError' }
}

export async function getAssistantSettings(signal?: AbortSignal): Promise<AssistantSettings> {
  return (await client.get('/settings/assistant', { signal })).data
}
export async function saveAssistantSettings(input: AssistantSettingsInput, signal?: AbortSignal): Promise<AssistantSettings> {
  return (await client.put('/settings/assistant', input, { signal })).data
}
export async function testAssistantSettings(input: AssistantSettingsInput, signal?: AbortSignal): Promise<{ success: boolean; message: string; model: string }> {
  return (await client.post('/settings/assistant/test', input, { signal, timeout: (input.timeout_seconds + 5) * 1000 })).data
}
export async function getAssistantStatus(signal?: AbortSignal): Promise<AssistantStatus> {
  return (await client.get('/assistant/status', { signal })).data
}
export async function listAssistantConversations(signal?: AbortSignal): Promise<AssistantConversation[]> {
  return (await client.get('/assistant/conversations', { signal })).data.conversations ?? []
}
export async function createAssistantConversation(signal?: AbortSignal): Promise<AssistantConversation> {
  return (await client.post('/assistant/conversations', {}, { signal })).data.conversation
}
export async function getAssistantConversation(id: string, signal?: AbortSignal): Promise<AssistantDetail> {
  return (await client.get(`/assistant/conversations/${encodeURIComponent(id)}`, { signal })).data
}
export async function resolveAssistantAction(id: string, decision: 'confirm' | 'reject', signal?: AbortSignal): Promise<AssistantAction> {
  const data = (await client.post(`/assistant/actions/${encodeURIComponent(id)}/${decision}`, {}, { signal })).data
  return data.action
}

// 一个网络分块可能包含半个事件或多个事件，不能按读取次数切分 JSON。
export function createAssistantEventParser(onEvent: (event: AssistantEvent) => void) {
  let buffer = ''
  let data: string[] = []
  const dispatch = () => {
    if (!data.length) return
    const value = data.join('\n')
    data = []
    if (value === '[DONE]') { onEvent({ type: 'done' }); return }
    let event: AssistantEvent
    try { event = JSON.parse(value) as AssistantEvent }
    catch { throw new AssistantStreamError('助手响应格式无效，请重试') }
    if (event?.type === 'start') return
    if (!event || !['delta', 'tool_start', 'tool_end', 'action_proposed', 'done', 'error'].includes(event.type)) throw new AssistantStreamError('助手响应格式无效，请重试')
    onEvent(event)
  }
  const line = (value: string) => {
    if (!value) dispatch()
    else if (value.startsWith('data:')) data.push(value.slice(5).replace(/^ /, ''))
  }
  return {
    push(chunk: string) {
      buffer += chunk
      let end: number
      while ((end = buffer.indexOf('\n')) >= 0) {
        line(buffer.slice(0, end).replace(/\r$/, ''))
        buffer = buffer.slice(end + 1)
      }
    },
    finish() {
      if (buffer) line(buffer.replace(/\r$/, ''))
      buffer = ''
      dispatch()
    },
  }
}

export async function streamAssistantMessage(id: string, content: string, projectID: string, signal: AbortSignal, onEvent: (event: AssistantEvent) => void): Promise<void> {
  const response = await fetch(`/api/v1/assistant/conversations/${encodeURIComponent(id)}/messages`, {
    method: 'POST', credentials: 'include', signal,
    headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream', 'X-EDO-Project-ID': projectID },
    body: JSON.stringify({ content }),
  })
  if (!response.ok) {
    const error = await response.json().catch(() => null) as { message?: string } | null
    throw new AssistantStreamError(error?.message || '助手请求失败，请稍后重试')
  }
  if (!response.body || !response.headers.get('content-type')?.includes('text/event-stream')) throw new AssistantStreamError('助手响应格式无效，请重试')
  const reader = response.body.getReader(), decoder = new TextDecoder()
  let completed = false
  const parser = createAssistantEventParser(event => {
    if (signal.aborted || completed) return
    if (event.type === 'done' || event.type === 'error') completed = true
    onEvent(event)
  })
  try {
    while (true) {
      const { value, done } = await reader.read()
      if (signal.aborted) return
      if (done) break
      parser.push(decoder.decode(value, { stream: true }))
    }
    parser.push(decoder.decode())
    parser.finish()
    if (!completed && !signal.aborted) throw new AssistantStreamError('助手连接已中断，输入已保留，请重试')
  } finally {
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}
