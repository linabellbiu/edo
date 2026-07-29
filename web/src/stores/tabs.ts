import { ref } from 'vue'
import { defineStore } from 'pinia'
import type { RouteLocationNormalizedLoaded, Router } from 'vue-router'

export interface PageTab {
  key: string
  title: string
  path: string
  pinned: boolean
}

const storageKey = 'zrt.tabs'
const historyStorageKey = 'zrt.navigation-history'
const overview: PageTab = { key: '/', title: 'nav.overview', path: '/', pinned: true }

export interface PageVisit {
  title: string
  path: string
}

function pageKey(path: string) {
  try {
    return new URL(path, 'http://zrt.local').pathname
  } catch {
    return path.split(/[?#]/, 1)[0] || '/'
  }
}

function currentTabPath(path: string) {
  try {
    const target = new URL(path, 'http://zrt.local')
    if (target.pathname !== '/infrastructure') return path
    target.pathname = '/hosts'
    target.searchParams.set('view', 'resources')
    return `${target.pathname}${target.search}${target.hash}`
  } catch {
    return path
  }
}

function restoreTabs(): PageTab[] {
  try {
    const value = JSON.parse(sessionStorage.getItem(storageKey) || '[]') as PageTab[]
    const valid = value.filter((tab) => tab?.key?.startsWith('/') && tab?.path?.startsWith('/'))
    const restored = new Map<string, PageTab>()
    for (const tab of valid) {
      const path = currentTabPath(tab.path)
      const key = pageKey(path)
      if (key === '/') continue
      const current = restored.get(key)
      restored.set(key, {
        key,
        title: pageKey(tab.path) === '/infrastructure' ? 'nav.hosts' : tab.title,
        path,
        pinned: Boolean(tab.pinned || current?.pinned),
      })
    }
    return [overview, ...restored.values()]
  } catch {
    return [overview]
  }
}

function restoreHistory(): PageVisit[] {
  try {
    const value = JSON.parse(sessionStorage.getItem(historyStorageKey) || '[]') as PageVisit[]
    return value
      .filter((item) => item?.path?.startsWith('/') && typeof item.title === 'string')
      .slice(-40)
      .map((item) => ({ ...item, path: currentTabPath(item.path) }))
  } catch {
    return []
  }
}

export const useTabsStore = defineStore('tabs', () => {
  const tabs = ref<PageTab[]>(restoreTabs())
  const history = ref<PageVisit[]>(restoreHistory())

  function persist() {
    try { sessionStorage.setItem(storageKey, JSON.stringify(tabs.value)) } catch { /* 当前会话仍保留标签。 */ }
  }

  function persistHistory() {
    try { sessionStorage.setItem(historyStorageKey, JSON.stringify(history.value)) } catch { /* 当前会话仍保留访问顺序。 */ }
  }

  function visit(route: RouteLocationNormalizedLoaded) {
    if (route.meta.public) return
    const entry = { title: String(route.meta.title || 'nav.overview'), path: currentTabPath(route.fullPath) }
    const current = history.value.at(-1)
    if (current?.path === entry.path) {
      current.title = entry.title
    } else {
      history.value.push(entry)
      if (history.value.length > 40) history.value.splice(0, history.value.length - 40)
    }
    persistHistory()
  }

  function currentHistoryIndex(currentPath: string) {
    const path = currentTabPath(currentPath)
    for (let index = history.value.length - 1; index >= 0; index -= 1) {
      if (history.value[index]?.path === path) return index
    }
    return -1
  }

  function previousHistoryIndex(currentPath: string) {
    const currentIndex = currentHistoryIndex(currentPath)
    if (currentIndex < 1) return -1
    const current = history.value[currentIndex]?.path
    let index = currentIndex - 1
    while (index >= 0 && history.value[index]?.path === current) index -= 1
    return index
  }

  function recent(currentPath: string, limit = 8) {
    const result: PageVisit[] = []
    const seen = new Set<string>([currentTabPath(currentPath)])
    for (let index = currentHistoryIndex(currentPath) - 1; index >= 0 && result.length < limit; index -= 1) {
      const item = history.value[index]
      if (!item || seen.has(item.path)) continue
      seen.add(item.path)
      result.push(item)
    }
    return result
  }

  function canGoBack(currentPath: string) {
    return previousHistoryIndex(currentPath) >= 0
  }

  async function back(router: Router, currentPath: string) {
    const currentIndex = currentHistoryIndex(currentPath)
    const targetIndex = previousHistoryIndex(currentPath)
    const target = history.value[targetIndex]
    if (currentIndex < 0 || !target) return
    history.value.splice(targetIndex + 1)
    persistHistory()
    if (window.history.state?.back === target.path) {
      router.back()
      return
    }
    await router.push(target.path)
  }

  function add(route: RouteLocationNormalizedLoaded) {
    if (route.meta.public || route.name === 'pipeline-editor') return
    const key = route.path
    const title = String(route.meta.title || 'nav.overview')
    let legacyPinned = false
    if (key === '/hosts') {
      const legacyIndex = tabs.value.findIndex((tab) => tab.key === '/infrastructure')
      if (legacyIndex >= 0) {
        legacyPinned = Boolean(tabs.value[legacyIndex]?.pinned)
        tabs.value.splice(legacyIndex, 1)
      }
    }
    const exists = tabs.value.find((tab) => tab.key === key)
    if (exists) {
      exists.title = title
      exists.path = route.fullPath
      exists.pinned ||= legacyPinned
    } else {
      tabs.value.push({ key, title, path: route.fullPath, pinned: legacyPinned })
    }
    persist()
  }

  async function close(key: string, router: Router, currentPath: string) {
    const index = tabs.value.findIndex((tab) => tab.key === key)
    if (index < 0 || tabs.value[index]?.pinned) return
    const closingCurrent = currentPath === key
    tabs.value.splice(index, 1)
    persist()
    if (closingCurrent) await router.push(tabs.value[Math.max(0, index - 1)]?.path || '/')
  }

  function togglePin(key: string) {
    const tab = tabs.value.find((item) => item.key === key)
    if (!tab || tab.key === '/') return
    tab.pinned = !tab.pinned
    tabs.value = [overview, ...tabs.value.filter((item) => item.key !== '/').sort((a, b) => Number(b.pinned) - Number(a.pinned))]
    persist()
  }

  function closeOthers(key: string) {
    tabs.value = tabs.value.filter((tab) => tab.pinned || tab.key === key)
    persist()
  }

  function closeSide(key: string, side: 'left' | 'right') {
    const index = tabs.value.findIndex((tab) => tab.key === key)
    tabs.value = tabs.value.filter((tab, itemIndex) => tab.pinned || (side === 'left' ? itemIndex >= index : itemIndex <= index))
    persist()
  }

  function closeAll() {
    tabs.value = tabs.value.filter((tab) => tab.pinned)
    persist()
  }

  return { tabs, history, visit, recent, canGoBack, back, add, close, togglePin, closeOthers, closeSide, closeAll }
})
