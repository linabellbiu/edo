<script setup lang="ts">
import { computed, ref } from 'vue'
import { ConfigProvider, Segmented, theme } from 'ant-design-vue'

const query = new URLSearchParams(location.search)
const dark = ref(query.get('theme') === 'dark')
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

const settings = ref('account')
const settingsOptions = [
  { value: 'account', label: '修改密码' },
  { value: 'general', label: '安全与接入' },
  { value: 'interface', label: '界面设置' },
  { value: 'assistant', label: '助手设置' },
  { value: 'variables', label: '环境变量' },
  { value: 'logs', label: '日志设置' },
  { value: 'storage', label: '存储目录' },
  { value: 'identity', label: '登录方式' },
]
const access = ref('users')
const accessOptions = [
  { value: 'users', label: '用户管理' },
  { value: 'departments', label: '部门管理' },
  { value: 'roles', label: '角色与权限' },
  { value: 'audit', label: '审计日志' },
]
const range = ref(900)
const rangeOptions = [
  { value: 900, label: '15 分钟' },
  { value: 3600, label: '1 小时' },
  { value: 21600, label: '6 小时' },
  { value: 86400, label: '24 小时' },
  { value: 259200, label: '3 天' },
]
const status = ref('all')
const statusOptions = [
  { value: 'all', label: '全部' },
  { value: 'active', label: '启用' },
  { value: 'inactive', label: '停用' },
]
const mode = ref('entries')
const modeOptions = [
  { value: 'entries', label: '条目编辑' },
  { value: 'tabs', label: '分栏编辑' },
]
const permission = ref('inherit')
const permissionOptions = [
  { value: 'inherit', label: '继承' },
  { value: 'allow', label: '允许' },
  { value: 'deny', label: '拒绝' },
]

function toggleTheme() {
  dark.value = !dark.value
  document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'
}
</script>

<template>
  <ConfigProvider :theme="antTheme">
    <main class="segmented-preview">
      <header>
        <h1>EDO 分段页签视觉对照</h1>
        <button type="button" @click="toggleTheme">切换主题</button>
      </header>

      <section class="preview-card">
        <h2>页面页签（系统设置）</h2>
        <Segmented v-model:value="settings" :options="settingsOptions" class="edo-page-tabs" aria-label="系统设置页面" />
        <h2>页面页签（访问控制）</h2>
        <Segmented v-model:value="access" :options="accessOptions" class="edo-page-tabs" aria-label="访问控制页面" />
      </section>

      <section class="preview-card">
        <h2>筛选与范围（默认尺寸）</h2>
        <div class="inline-row">
          <Segmented v-model:value="range" :options="rangeOptions" aria-label="时间范围" />
          <Segmented v-model:value="status" :options="statusOptions" aria-label="状态筛选" />
          <Segmented v-model:value="mode" :options="modeOptions" size="small" aria-label="编辑方式" />
          <Segmented v-model:value="permission" :options="permissionOptions" size="small" aria-label="权限动作" />
        </div>
        <h2>禁用与长选项</h2>
        <div class="inline-row">
          <Segmented value="all" :options="statusOptions" disabled aria-label="禁用的状态筛选" />
          <Segmented value="b" :options="[{ value: 'a', label: '按命名空间聚合' }, { value: 'b', label: '按工作负载聚合' }, { value: 'c', label: '按节点聚合' }]" size="small" aria-label="聚合方式" />
        </div>
      </section>
    </main>
  </ConfigProvider>
</template>

<style scoped>
.segmented-preview { display: grid; gap: 16px; padding: 24px; background: var(--edo-bg); min-height: 100vh; }
header { display: flex; align-items: center; justify-content: space-between; }
header h1 { margin: 0; font-size: 16px; }
header button { height: 30px; padding: 0 12px; border: 1px solid var(--edo-border); border-radius: 8px; background: var(--edo-surface); color: var(--edo-text); cursor: pointer; }
.preview-card { padding: 16px; border: 1px solid var(--edo-border); border-radius: 12px; background: var(--edo-surface); }
.preview-card h2 { margin: 12px 0 8px; color: var(--edo-muted); font-size: 12px; font-weight: 500; }
.preview-card h2:first-child { margin-top: 0; }
.inline-row { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
</style>
