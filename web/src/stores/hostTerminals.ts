import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import type { InfrastructureHost } from '@/api/infrastructure'
import { useAuthStore } from '@/stores/auth'

export type HostTerminalState = 'connecting' | 'connected' | 'closed' | 'error'
export interface HostTerminalSession {
  id: string
  hostId: string
  hostName: string
  path: string
  state: HostTerminalState
  context: string
}
export const maxHostTerminals = 8
export function hostSupportsTerminal(host: InfrastructureHost) {
  return host.is_active && host.capabilities.some(capability => capability.kind === (host.mode === 'local' ? 'local_exec' : 'ssh') && capability.status === 'ready')
}
export function terminalActorContext(user: { id: string; department_id?: string; project_id?: string; is_superuser: boolean; permissions: string[] } | null, project: string) {
  return JSON.stringify([user?.id ?? '', user?.project_id ?? '', user?.department_id ?? '', project, Boolean(user?.is_superuser), [...(user?.permissions ?? [])].sort()])
}

export const useHostTerminalsStore = defineStore('host-terminals', () => {
  const auth = useAuthStore()
  const sessions = ref<HostTerminalSession[]>([])
  const activeId = ref(''), minimized = ref(true)
  let sequence = 0
  const project = () => typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? ''
  const context = () => terminalActorContext(auth.user, project())
  const active = computed(() => sessions.value.find(session => session.id === activeId.value))
  const groups = computed(() => {
    const groups = new Map<string, { id: string; name: string; sessions: HostTerminalSession[] }>()
    for (const session of sessions.value) {
      let group = groups.get(session.hostId)
      if (!group) { group = { id: session.hostId, name: session.hostName, sessions: [] }; groups.set(session.hostId, group) }
      group.sessions.push(session)
    }
    return [...groups.values()]
  })
  function clear() { sessions.value = []; activeId.value = ''; minimized.value = true }
  function allowed() { return Boolean(auth.user) && auth.canAny(['host.terminal.open']) }
  function syncContext() {
    if (!allowed() || sessions.value.some(session => session.context !== context())) clear()
  }
  function open(host: InfrastructureHost, forceNew = false): { id?: string; error?: string } {
    syncContext()
    if (!allowed()) return { error: '没有打开主机终端的权限' }
    if (!hostSupportsTerminal(host)) return { error: '主机终端当前不可用' }
    const previous = !forceNew && sessions.value.find(session => session.hostId === host.id && !['closed', 'error'].includes(session.state))
    if (previous) { activate(previous.id); return { id: previous.id } }
    if (sessions.value.length >= maxHostTerminals) return { error: '最多同时保留 8 个主机终端，请先关闭不再使用的会话' }
    const id = `host-terminal-${++sequence}`
    const parameters = new URLSearchParams({ project_id: project() })
    sessions.value.push({ id, hostId: host.id, hostName: host.name, path: `/api/v1/terminals/hosts/${encodeURIComponent(host.id)}/ws?${parameters}`, state: 'connecting', context: context() })
    activeId.value = id; minimized.value = false
    return { id }
  }
  function activate(id: string) { syncContext(); if (sessions.value.some(session => session.id === id)) { activeId.value = id; minimized.value = false } }
  function duplicate(id: string): { id?: string; error?: string } {
    syncContext()
    const current = sessions.value.find(session => session.id === id)
    if (!current || !allowed()) return { error: '当前终端不可用，请从主机列表重新打开' }
    if (sessions.value.length >= maxHostTerminals) return { error: '最多同时保留 8 个主机终端，请先关闭不再使用的会话' }
    const next = { ...current, id: `host-terminal-${++sequence}`, state: 'connecting' as const }
    sessions.value.push(next); activeId.value = next.id; minimized.value = false
    return { id: next.id }
  }
  function minimize() { minimized.value = true }
  function close(id: string) {
    const index = sessions.value.findIndex(session => session.id === id)
    if (index < 0) return
    sessions.value.splice(index, 1)
    if (activeId.value === id) activeId.value = sessions.value[Math.min(index, sessions.value.length - 1)]?.id ?? ''
    if (!sessions.value.length) minimized.value = true
  }
  function updateState(id: string, state: HostTerminalState) { const session = sessions.value.find(session => session.id === id); if (session) session.state = state }
  watch(() => context(), syncContext, { flush: 'sync' })
  return { sessions, activeId, minimized, active, groups, open, duplicate, activate, minimize, close, clear, syncContext, updateState }
})
