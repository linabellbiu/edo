import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { theme as antTheme } from 'ant-design-vue'

import i18n, { type AppLocale } from '@/locales'

export type ThemeMode = 'light' | 'dark'

const themeKey = 'edo.theme'
const localeKey = 'edo.locale'
const sidebarKey = 'edo.sidebar.collapsed'

function stored<T extends string>(key: string, allowed: readonly T[], fallback: T): T {
  try {
    const value = localStorage.getItem(key) as T | null
    return value && allowed.includes(value) ? value : fallback
  } catch {
    return fallback
  }
}

function initialTheme(): ThemeMode {
  const system = window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  return stored(themeKey, ['light', 'dark'], system)
}

export const usePreferencesStore = defineStore('preferences', () => {
  const theme = ref<ThemeMode>(initialTheme())
  const locale = ref<AppLocale>(stored(localeKey, ['zh-CN', 'en-US'], 'zh-CN'))
  const sidebarCollapsed = ref(stored(sidebarKey, ['true', 'false'], 'false') === 'true')
  const antAlgorithm = computed(() => theme.value === 'dark' ? antTheme.darkAlgorithm : antTheme.defaultAlgorithm)

  function applyTheme(value: ThemeMode) {
    theme.value = value
    document.documentElement.dataset.theme = value
    document.documentElement.style.colorScheme = value
    try { localStorage.setItem(themeKey, value) } catch { /* 当前会话仍可切换。 */ }
  }

  function toggleTheme() {
    applyTheme(theme.value === 'dark' ? 'light' : 'dark')
  }

  function setLocale(value: AppLocale) {
    locale.value = value
    i18n.global.locale.value = value
    document.documentElement.lang = value
    try { localStorage.setItem(localeKey, value) } catch { /* 当前会话仍可切换。 */ }
  }

  function toggleSidebar() {
    sidebarCollapsed.value = !sidebarCollapsed.value
    try { localStorage.setItem(sidebarKey, String(sidebarCollapsed.value)) } catch { /* 当前会话仍可切换。 */ }
  }

  applyTheme(theme.value)
  setLocale(locale.value)

  return { theme, locale, sidebarCollapsed, antAlgorithm, toggleTheme, setLocale, toggleSidebar }
})
