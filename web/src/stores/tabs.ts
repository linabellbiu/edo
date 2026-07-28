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
const overview: PageTab = { key: '/', title: 'nav.overview', path: '/', pinned: true }

function restoreTabs(): PageTab[] {
  try {
    const value = JSON.parse(sessionStorage.getItem(storageKey) || '[]') as PageTab[]
    const valid = value.filter((tab) => tab?.key?.startsWith('/') && tab?.path?.startsWith('/'))
    return [overview, ...valid.filter((tab) => tab.key !== '/')]
  } catch {
    return [overview]
  }
}

export const useTabsStore = defineStore('tabs', () => {
  const tabs = ref<PageTab[]>(restoreTabs())

  function persist() {
    try { sessionStorage.setItem(storageKey, JSON.stringify(tabs.value)) } catch { /* 当前会话仍保留标签。 */ }
  }

  function add(route: RouteLocationNormalizedLoaded) {
    if (route.meta.public || route.name === 'pipeline-editor') return
    const key = route.fullPath
    const title = String(route.meta.title || 'nav.overview')
    const exists = tabs.value.find((tab) => tab.key === key)
    if (exists) {
      exists.title = title
      exists.path = route.fullPath
    } else {
      tabs.value.push({ key, title, path: route.fullPath, pinned: false })
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

  return { tabs, add, close, togglePin, closeOthers, closeSide, closeAll }
})
