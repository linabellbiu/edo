import axios from 'axios'
import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

import {
  default as client,
  getCurrentUser,
  login as loginRequest,
  loginLDAP as loginLDAPRequest,
  logout as logoutRequest,
  type User,
} from '@/api/client'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const loaded = ref(false)
  const loadFailed = ref(false)
  const permissions = computed(() => new Set(user.value?.permissions ?? []))
  let revision = 0
  let permissionRevision = 0
  let loadPromise: Promise<void> | null = null
  let refresh: { subject: User; project: string; revision: number; cancelled: boolean; promise: Promise<void> } | undefined
  let schedulePermissionSync: (() => void) | undefined
  const selectedProject = () => localStorage.getItem('edo.project') ?? ''

  function invalidateSession() {
    revision++
    permissionRevision++
    loadPromise = null
    refresh = undefined
    user.value = null
  }

  async function ensureLoaded() {
    if (loaded.value) return
    if (loadPromise) return loadPromise

    const request = revision
    const pending = (async () => {
      try {
        await restoreProject(request)
        if (request !== revision) return
        const profile = await getCurrentUser()
        if (request !== revision) return
        user.value = profile
        loaded.value = true
        loadFailed.value = false
      } catch (error) {
        if (request !== revision) return
        if (axios.isAxiosError(error) && error.response?.status === 401) {
          user.value = null
          loaded.value = true
          loadFailed.value = false
          return
        }
        user.value = null
        loaded.value = true
        loadFailed.value = true
      }
    })().finally(() => {
      if (loadPromise === pending) loadPromise = null
    })
    loadPromise = pending
    return pending
  }

  async function restoreProject(request: number) {
    const {data}=await client.get<{projects:Array<{id:string;is_active:boolean}>}>('/projects')
    if (request !== revision) return
    const active=data.projects.filter(item=>item.is_active)
    const saved=localStorage.getItem('edo.project')
    if(!active.some(item=>item.id===saved)) {
      if(active[0])localStorage.setItem('edo.project',active[0].id)
      else localStorage.removeItem('edo.project')
    }
  }

  async function establishSession(authenticate: () => Promise<unknown>) {
    invalidateSession()
    const request = revision
    await authenticate()
    if (request !== revision) return
    await restoreProject(request)
    if (request !== revision) return
    const profile = await getCurrentUser()
    if (request !== revision) return
    user.value = profile
    loaded.value = true
    loadFailed.value = false
  }

  async function login(username: string, password: string) {
    await establishSession(() => loginRequest(username, password))
  }

  async function loginLDAP(providerID: string, username: string, password: string) {
    await establishSession(() => loginLDAPRequest(providerID, username, password))
  }

  async function logout() {
    invalidateSession()
    const request = revision
    try {
      await logoutRequest()
    } finally {
      if (request !== revision) return
      loaded.value = true
      loadFailed.value = false
    }
  }

  async function loginEmail(providerID: string, email: string, code: string) {
    await establishSession(() => client.post(`/auth/email/${encodeURIComponent(providerID)}/login`, { email, code }))
  }

  async function refreshPermissions() {
    const subject = user.value, project = selectedProject(), request = revision
    if (!subject || (subject.project_id ?? '') !== project) return
    if (refresh?.subject === subject && refresh.project === project && refresh.revision === request) return refresh.promise
    const operation = { subject, project, revision: request, cancelled: false, promise: Promise.resolve() }
    const current = () => !operation.cancelled && request === revision && user.value === subject && selectedProject() === project
    const pending = (async () => {
      // 每轮最多一次尾随；持续拒绝合并到延后校准，不能形成无界请求循环。
      for (let attempt = 0; attempt < 2 && current(); attempt++) {
        const snapshot = permissionRevision
        try {
          const profile = await getCurrentUser()
          if (!current()) return
          // 请求期间收到新的拒绝，只串行核验最新权限，不应用旧快照或并发重放。
          if (snapshot !== permissionRevision) {
            if (attempt === 1) schedulePermissionSync?.()
            continue
          }
          // 身份变化必须重新登录，不能把新身份与旧页面的资源范围混合。
          if (profile.id !== subject.id || profile.department_id !== subject.department_id) {
            invalidateSession()
            loaded.value = true
            return
          }
          user.value = profile
          loadFailed.value = false
          return
        } catch (error) {
          if (!current()) return
          if (axios.isAxiosError(error) && error.response?.status === 401) {
            invalidateSession()
            loaded.value = true
            loadFailed.value = false
          } else if (snapshot !== permissionRevision) {
            if (attempt === 1) schedulePermissionSync?.()
            continue
          } else if (axios.isAxiosError(error) && error.response?.status === 403) {
            // 当前项目授权失效时清掉旧权限，但不退出仍可访问其他项目的会话。
            if (subject.permissions.length || subject.is_superuser) {
              user.value = { ...subject, permissions: [], is_superuser: false }
              window.dispatchEvent(new Event('edo:projects-changed'))
            }
          }
          // 网络失败不伪造撤权或登出；后续请求仍由服务端逐项鉴权。
          throw error
        }
      }
    })().finally(() => {
      if (refresh?.promise === pending) refresh = undefined
    })
    operation.promise = pending
    refresh = operation
    return pending
  }

  function startPermissionSync() {
    let disposed = false, lastAttempt = -Infinity
    let retry: number | undefined
    const sync = () => {
      if (disposed || !user.value || document.hidden || retry !== undefined) return
      const delay = 1000 - (Date.now() - lastAttempt)
      if (delay > 0) {
        // 拒绝通知不能被短期去重直接丢弃；合并成一次延后核验。
        if (retry === undefined) retry = window.setTimeout(() => { retry = undefined; sync() }, delay)
        return
      }
      lastAttempt = Date.now()
      void refreshPermissions().catch(() => { /* 原业务请求保留错误反馈，后台校准不重复弹窗。 */ })
    }
    const schedule = () => {
      if (!disposed && retry === undefined) retry = window.setTimeout(() => { retry = undefined; sync() }, 1000)
    }
    schedulePermissionSync = schedule
    const denied = (event: Event) => {
      if ((event as CustomEvent<{ project: string }>).detail?.project !== selectedProject()) return
      permissionRevision++
      // 在途核验会在响应后处理最新修订；合并突发拒绝，不另起并发请求。
      if (refresh?.subject === user.value && refresh.project === selectedProject() && refresh.revision === revision) return
      sync()
    }
    window.addEventListener('edo:permissions-stale', denied)
    window.addEventListener('focus', sync)
    document.addEventListener('visibilitychange', sync)
    // 只校准当前用户权限，不依赖业务通知权限，也不轮询业务列表。
    const interval = window.setInterval(sync, 60_000)
    return () => {
      if (disposed) return
      disposed = true
      if (schedulePermissionSync === schedule) schedulePermissionSync = undefined
      if (refresh) { refresh.cancelled = true; refresh = undefined }
      window.clearInterval(interval)
      if (retry !== undefined) window.clearTimeout(retry)
      window.removeEventListener('edo:permissions-stale', denied)
      window.removeEventListener('focus', sync)
      document.removeEventListener('visibilitychange', sync)
    }
  }

  function canAny(required: string[]) {
    if (user.value?.is_superuser || required.length === 0) return true
    return required.some((permission) => permissions.value.has(permission))
  }

  return { user, loaded, loadFailed, permissions, ensureLoaded, refreshPermissions, startPermissionSync, login, loginLDAP, loginEmail, logout, canAny }
})
