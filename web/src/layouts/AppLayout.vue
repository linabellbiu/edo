<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  Bell, ChevronDown, ChevronLeft, Command, Expand, Languages, LockKeyhole, LogOut,
  Maximize2, Menu, Moon, PanelLeftClose, PanelLeftOpen, RefreshCw, Search,
  Pin, Settings, Sun, X,
} from 'lucide-vue-next'

import { flatNavigation, navigation, type NavBranch, type NavItem } from '@/router/navigation'
import { useAuthStore } from '@/stores/auth'
import { usePreferencesStore } from '@/stores/preferences'
import { useTabsStore } from '@/stores/tabs'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const preferences = usePreferencesStore()
const tabsStore = useTabsStore()
const { t } = useI18n()

const searchOpen = ref(false)
const searchText = ref('')
const contentMaximized = ref(false)
const reloadKey = ref(0)
const mobileMenuOpen = ref(false)
const openKeys = ref<string[]>([])

const allowed = (item: NavItem) => auth.canAny(item.permissions)
const allowedBranches = (sectionIndex: number): NavBranch[] =>
  (navigation[sectionIndex]?.branches ?? [])
    .map((branch) => ({ ...branch, items: branch.items.filter(allowed) }))
    .filter((branch) => branch.items.length > 0)

const selectedKeys = computed(() => {
  const item = flatNavigation().find((candidate) => {
    const target = new URL(candidate.path, 'http://edo.local')
    if (target.pathname !== route.path) return false
    return [...target.searchParams].every(([key, value]) => (route.query[key] ?? '') === value)
  })
  return item ? [item.path] : []
})

const currentNavItem = computed(() => flatNavigation().find((candidate) => candidate.path === selectedKeys.value[0]))
const recentVisits = computed(() => tabsStore.recent(route.fullPath))
const canGoBack = computed(() => tabsStore.canGoBack(route.fullPath))

const breadcrumbs = computed(() => {
  for (const section of navigation) {
    const direct = section.items?.find((item) => item.path === selectedKeys.value[0])
    if (direct) return [{ label: direct.label }]
    for (const branch of section.branches ?? []) {
      const item = branch.items.find((candidate) => candidate.path === selectedKeys.value[0])
      if (item) return [
        section.label ? { label: section.label } : null,
        { label: branch.label },
        { label: item.label },
      ].filter((value): value is { label: string } => Boolean(value))
    }
  }
  return [{ label: String(route.meta.title || 'nav.overview') }]
})

const searchableItems = computed(() => flatNavigation()
  .filter(allowed)
  .filter((item) => t(item.label).toLowerCase().includes(searchText.value.trim().toLowerCase())))

function tabIcon(path: string) {
  const target = new URL(path, 'http://edo.local')
  return flatNavigation().find((item) => {
    const candidate = new URL(item.path, 'http://edo.local')
    if (candidate.pathname !== target.pathname) return false
    return [...candidate.searchParams].every(([key, value]) => target.searchParams.get(key) === value)
  })?.icon
}

function navigate(path: string) {
  mobileMenuOpen.value = false
  void router.push(path)
}

function navigateHistory(path: string) {
  void router.push(path)
}

function openSearch() {
  searchText.value = ''
  searchOpen.value = true
}

function toggleFullscreen() {
  if (document.fullscreenElement) void document.exitFullscreen()
  else void document.documentElement.requestFullscreen()
}

async function signOut() {
  await auth.logout()
  await router.replace('/login')
}

function reloadPage() {
  reloadKey.value += 1
}

function tabMenuAction(action: string, key: string, path: string) {
  if (action === 'close') void tabsStore.close(key, router, route.path)
  if (action === 'pin') tabsStore.togglePin(key)
  if (action === 'maximize') contentMaximized.value = !contentMaximized.value
  if (action === 'reload') reloadPage()
  if (action === 'new') window.open(path, '_blank', 'noopener,noreferrer')
  if (action === 'left') tabsStore.closeSide(key, 'left')
  if (action === 'right') tabsStore.closeSide(key, 'right')
  if (action === 'others') tabsStore.closeOthers(key)
  if (action === 'all') tabsStore.closeAll()
}

function keyboard(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault()
    openSearch()
  }
  if (event.key === 'Escape' && contentMaximized.value) contentMaximized.value = false
}

watch(() => route.fullPath, () => {
  tabsStore.visit(route)
  tabsStore.add(route)
  const branch = navigation.flatMap((section) => section.branches ?? [])
    .find((item) => item.items.some((candidate) => candidate.path === selectedKeys.value[0]))
  if (branch && !openKeys.value.includes(branch.key)) openKeys.value.push(branch.key)
}, { immediate: true })

watch(searchOpen, async (open) => {
  if (!open) return
  await nextTick()
  document.querySelector<HTMLInputElement>('.command-search input')?.focus()
})

onMounted(() => window.addEventListener('keydown', keyboard))
onBeforeUnmount(() => window.removeEventListener('keydown', keyboard))
</script>

<template>
  <div class="vben-app" :class="{ 'content-maximized': contentMaximized, 'sidebar-collapsed': preferences.sidebarCollapsed }">
    <aside class="vben-sidebar" :class="{ collapsed: preferences.sidebarCollapsed, 'mobile-open': mobileMenuOpen }">
      <button class="mobile-close" type="button" aria-label="关闭导航" @click="mobileMenuOpen = false"><X /></button>
      <div class="vben-logo" @click="navigate('/')">
        <span class="logo-mark"><span>Z</span></span>
        <strong>EDO</strong>
      </div>

      <nav class="vben-navigation">
        <a-menu
          :selected-keys="selectedKeys"
          :open-keys="preferences.sidebarCollapsed ? [] : openKeys"
          mode="inline"
          :inline-collapsed="preferences.sidebarCollapsed"
          @open-change="(keys: string[]) => openKeys = keys"
        >
          <a-menu-item v-for="item in navigation[0]?.items?.filter(allowed)" :key="item.path" @click="navigate(item.path)">
            <template #icon><component :is="item.icon" :size="18" /></template>
            {{ t(item.label) }}
          </a-menu-item>
        </a-menu>

        <template v-for="(section, sectionIndex) in navigation.slice(1)" :key="section.label">
          <div v-if="!preferences.sidebarCollapsed" class="navigation-section-title">{{ section.label ? t(section.label) : '' }}</div>
          <a-menu
            :selected-keys="selectedKeys"
            :open-keys="preferences.sidebarCollapsed ? [] : openKeys"
            mode="inline"
            :inline-collapsed="preferences.sidebarCollapsed"
            @open-change="(keys: string[]) => openKeys = keys"
          >
            <a-sub-menu v-for="branch in allowedBranches(sectionIndex + 1)" :key="branch.key">
              <template #icon><component :is="branch.icon" :size="18" /></template>
              <template #title>{{ t(branch.label) }}</template>
              <a-menu-item v-for="item in branch.items" :key="item.path" @click="navigate(item.path)">
                <template #icon><component :is="item.icon" :size="16" /></template>
                {{ t(item.label) }}
              </a-menu-item>
            </a-sub-menu>
          </a-menu>
        </template>
      </nav>

      <button class="sidebar-collapse" type="button" @click="preferences.toggleSidebar()">
        <PanelLeftOpen v-if="preferences.sidebarCollapsed" :size="18" />
        <PanelLeftClose v-else :size="18" />
        <span>收起导航</span>
      </button>
    </aside>

    <div class="mobile-backdrop" :class="{ visible: mobileMenuOpen }" @click="mobileMenuOpen = false" />

    <main class="vben-main">
      <header class="vben-header">
        <div class="header-left">
          <button class="mobile-menu-button" type="button" aria-label="打开导航" @click="mobileMenuOpen = true"><Menu /></button>
          <button class="desktop-collapse" type="button" aria-label="切换导航" @click="preferences.toggleSidebar()"><Menu /></button>
          <div class="breadcrumb-history" :class="{ disabled: !canGoBack }">
            <a-tooltip :title="t('common.back')"><button type="button" :disabled="!canGoBack" @click="tabsStore.back(router,route.fullPath)"><ChevronLeft /></button></a-tooltip>
            <a-dropdown :disabled="recentVisits.length===0" placement="bottomLeft" :trigger="['click']">
              <button type="button" :disabled="recentVisits.length===0" :aria-label="t('common.history')"><ChevronDown /></button>
              <template #overlay>
                <a-menu class="navigation-history-menu" @click="({key}:{key:string})=>navigateHistory(key)">
                  <a-menu-item v-for="item in recentVisits" :key="item.path">
                    <span class="navigation-history-item"><component :is="tabIcon(item.path)" v-if="tabIcon(item.path)"/><span><strong>{{ t(item.title) }}</strong><small>{{ item.path }}</small></span></span>
                  </a-menu-item>
                </a-menu>
              </template>
            </a-dropdown>
          </div>
          <a-breadcrumb>
            <a-breadcrumb-item v-for="(item, index) in breadcrumbs" :key="item.label">
              <span class="breadcrumb-label" :class="{ current: index === breadcrumbs.length - 1 }">
                <component :is="currentNavItem?.icon" v-if="index === breadcrumbs.length - 1 && currentNavItem?.icon" :size="16" />
                {{ t(item.label) }}
              </span>
            </a-breadcrumb-item>
          </a-breadcrumb>
        </div>

        <div class="header-actions">
          <button class="header-search" type="button" @click="openSearch">
            <Search :size="17" /><span>{{ t('common.search') }}</span><kbd><Command :size="12" /> K</kbd>
          </button>
          <a-tooltip :title="t('common.theme')"><button class="icon-button" type="button" @click="preferences.toggleTheme()"><Sun v-if="preferences.theme === 'dark'" /><Moon v-else /></button></a-tooltip>
          <a-dropdown placement="bottomRight">
            <a-tooltip :title="t('common.language')"><button class="icon-button" type="button"><Languages /></button></a-tooltip>
            <template #overlay><a-menu :selected-keys="[preferences.locale]" @click="({ key }: { key: string }) => preferences.setLocale(key as 'zh-CN' | 'en-US')"><a-menu-item key="zh-CN">简体中文</a-menu-item><a-menu-item key="en-US">English</a-menu-item></a-menu></template>
          </a-dropdown>
          <a-tooltip :title="t('common.fullscreen')"><button class="icon-button optional-action" type="button" @click="toggleFullscreen"><Expand /></button></a-tooltip>
          <a-tooltip :title="t('common.refresh')"><button class="icon-button optional-action" type="button" @click="reloadPage"><RefreshCw /></button></a-tooltip>
          <button class="icon-button optional-action" type="button" aria-label="通知"><Bell /></button>
          <a-dropdown placement="bottomRight">
            <button class="avatar-button" type="button"><span>{{ (auth.user?.nickname || auth.user?.username || 'Z').slice(0, 1).toUpperCase() }}</span><i /></button>
            <template #overlay>
              <a-menu>
                <a-menu-item disabled><Settings :size="15" /> {{ auth.user?.nickname || auth.user?.username }}</a-menu-item>
                <a-menu-divider />
                <a-menu-item @click="navigate('/settings')"><LockKeyhole :size="15" /> 修改密码</a-menu-item>
                <a-menu-item danger @click="signOut"><LogOut :size="15" /> {{ t('common.logout') }}</a-menu-item>
              </a-menu>
            </template>
          </a-dropdown>
        </div>
      </header>

      <div class="vben-tabs">
        <div class="tab-scroller">
          <a-dropdown v-for="tab in tabsStore.tabs" :key="tab.key" :trigger="['contextmenu']">
            <div
              class="page-tab"
              :class="{ active: route.path === tab.key, pinned: tab.pinned }"
              role="button"
              tabindex="0"
              @click="navigate(tab.path)"
              @keydown.enter.prevent="navigate(tab.path)"
              @keydown.space.prevent="navigate(tab.path)"
            >
              <component :is="tabIcon(tab.path)" v-if="tabIcon(tab.path)" class="tab-route-icon" :size="16" />
              <span>{{ t(tab.title) }}</span>
              <button v-if="tab.pinned" class="tab-action pin-action" type="button" aria-label="已固定" :disabled="tab.key === '/'" @click.stop="tabsStore.togglePin(tab.key)"><Pin :size="14" /></button>
              <button v-else class="tab-action" type="button" aria-label="关闭标签" @click.stop="tabsStore.close(tab.key, router, route.path)"><X :size="14" /></button>
            </div>
            <template #overlay>
              <a-menu @click="({ key }: { key: string }) => tabMenuAction(key, tab.key, tab.path)">
                <a-menu-item key="close" :disabled="tab.pinned">关闭</a-menu-item>
                <a-menu-item key="pin">{{ tab.pinned ? '取消固定' : '固定' }}</a-menu-item>
                <a-menu-item key="maximize">{{ contentMaximized ? '退出最大化' : '最大化' }}</a-menu-item>
                <a-menu-item key="reload">重新加载</a-menu-item>
                <a-menu-item key="new">新窗口打开</a-menu-item>
                <a-menu-divider />
                <a-menu-item key="left">关闭左侧</a-menu-item>
                <a-menu-item key="right">关闭右侧</a-menu-item>
                <a-menu-item key="others">关闭其他</a-menu-item>
                <a-menu-item key="all">关闭全部</a-menu-item>
              </a-menu>
            </template>
          </a-dropdown>
        </div>
        <button class="tabs-extra" type="button" @click="reloadPage"><RefreshCw /></button>
        <button v-if="contentMaximized" class="tabs-extra" type="button" @click="contentMaximized = false"><ChevronLeft /></button>
      </div>

      <section class="vben-content">
        <RouterView v-slot="{ Component }">
          <Transition name="page-fade" mode="out-in">
            <component :is="Component" :key="`${route.path}:${reloadKey}`" />
          </Transition>
        </RouterView>
      </section>
    </main>

    <a-modal v-model:open="searchOpen" :footer="null" :closable="false" width="620px" wrap-class-name="command-modal">
      <div class="command-search"><Search /><input v-model="searchText" :placeholder="`${t('common.search')}...`" /></div>
      <div class="command-results">
        <button v-for="item in searchableItems" :key="item.path" type="button" @click="searchOpen = false; navigate(item.path)">
          <component :is="item.icon" :size="17" /><span>{{ t(item.label) }}</span><kbd>↵</kbd>
        </button>
        <a-empty v-if="searchableItems.length === 0" description="没有匹配页面" />
      </div>
    </a-modal>
  </div>
</template>
