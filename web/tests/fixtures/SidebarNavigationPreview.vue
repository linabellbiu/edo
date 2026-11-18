<script setup lang="ts">
import { computed, ref } from 'vue'
import { ConfigProvider, theme } from 'ant-design-vue'
import { navigation } from '@/router/navigation'
import SidebarNavigation, { type SidebarSection } from '@/components/SidebarNavigation.vue'

const query = new URLSearchParams(location.search)
const dark = ref(query.get('theme') === 'dark')
const collapsed = ref(query.get('collapsed') === '1')
const selected = ref(query.get('selected') ?? '/settings')
const openKeys = ref<string[]>((query.get('open') ?? 'security').split(',').filter(Boolean))
if (query.get('readonly') === '1') openKeys.value = openKeys.value.filter(key => key !== 'security' || selected.value.startsWith('/setting'))
document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'

const antTheme = computed(() => ({
  token: {
    colorPrimary: '#4f6ef7',
    borderRadius: 6,
    borderRadiusLG: 8,
    colorBgLayout: dark.value ? '#0f1014' : '#f5f6f8',
    colorBgContainer: dark.value ? '#17181d' : '#ffffff',
    colorBorderSecondary: dark.value ? '#292b33' : '#e9eaee',
  },
  algorithm: dark.value ? theme.darkAlgorithm : theme.defaultAlgorithm,
}))

const overviewItems = computed(() => navigation[0]?.items ?? [])
const sections = computed<SidebarSection[]>(() => navigation.slice(1).map((section, index) => ({
  key: section.label ?? `section-${index}`,
  branches: section.branches ?? [],
})))

function toggleTheme() {
  dark.value = !dark.value
  document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'
}
function toggleBranch(key: string) {
  openKeys.value = openKeys.value.includes(key) ? openKeys.value.filter(value => value !== key) : [...openKeys.value, key]
}
function select(path: string) {
  selected.value = path
}
</script>

<template>
  <ConfigProvider :theme="antTheme">
  <div class="sidebar-preview" :class="{ 'preview-collapsed': collapsed }">
    <aside class="vben-sidebar" :class="{ collapsed }">
      <div class="vben-logo"><span class="logo-mark"><span>Z</span></span><strong>EDO</strong></div>
      <nav class="vben-navigation" aria-label="侧栏导航预览">
        <SidebarNavigation
          :overview-items="overviewItems"
          :sections="sections"
          :selected-keys="[selected]"
          :open-keys="openKeys"
          :collapsed="collapsed"
          @navigate="select"
          @open-change="(keys: string[]) => (openKeys = keys)"
          @toggle-branch="toggleBranch"
        />
      </nav>
      <button class="sidebar-collapse" type="button" @click="collapsed = !collapsed">
        <span>{{ collapsed ? '展开导航' : '收起导航' }}</span>
      </button>
    </aside>
    <main class="preview-panel">
      <h1>侧栏导航预览</h1>
      <p>选中：{{ selected }}　展开：{{ openKeys.join(' / ') || '无' }}　主题：{{ dark ? '深色' : '浅色' }}　形态：{{ collapsed ? '折叠' : '展开' }}</p>
      <div class="preview-actions">
        <button type="button" @click="toggleTheme">切换主题</button>
        <button type="button" @click="collapsed = !collapsed">切换折叠</button>
        <button type="button" @click="select('/settings')">选中系统设置</button>
        <button type="button" @click="select('/system-monitor')">选中系统监控</button>
        <button type="button" @click="select('/access?view=audit')">选中审计日志</button>
      </div>
      <p class="preview-hint">参数：?theme=dark&collapsed=1&selected=/settings&open=security,infrastructure</p>
    </main>
  </div>
  </ConfigProvider>
</template>

<style scoped>
.sidebar-preview { display: grid; min-height: 100vh; grid-template-columns: 224px minmax(0, 1fr); background: var(--edo-bg); }
.sidebar-preview.preview-collapsed { grid-template-columns: 64px minmax(0, 1fr); }
.preview-panel { padding: 24px; }
.preview-panel h1 { margin: 0 0 8px; font-size: 18px; }
.preview-panel p { margin: 0 0 12px; color: var(--edo-muted); font-size: 13px; }
.preview-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.preview-actions button { height: 32px; padding: 0 12px; border: 1px solid var(--edo-border); border-radius: 8px; background: var(--edo-surface); color: var(--edo-text); cursor: pointer; }
.preview-actions button:hover { color: var(--edo-primary); border-color: var(--edo-primary); }
.preview-hint { margin-top: 16px; font-size: 12px; }
</style>