<script setup lang="ts">
import { computed, ref } from 'vue'
import { ConfigProvider, Modal, theme } from 'ant-design-vue'
import TerminalDrawer from '/src/components/TerminalDrawer.vue'

const query = new URLSearchParams(location.search)
const dark = ref(query.get('theme') === 'dark')
document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'
const open = ref(true), events = ref<string[]>([]), editorOpen = ref(false)
const antTheme = computed(() => ({
  token: { colorPrimary: '#4f6ef7', borderRadius: 6, borderRadiusLG: 8, colorBgLayout: dark.value ? '#0f1014' : '#f5f6f8', colorBgContainer: dark.value ? '#17181d' : '#ffffff' },
  algorithm: dark.value ? theme.darkAlgorithm : theme.defaultAlgorithm,
}))
function record(value: boolean) {
  events.value.push(value ? 'open' : 'closed')
  open.value = value
}
</script>

<template>
  <ConfigProvider :theme="antTheme">
    <main class="esc-preview">
      <header><h1>终端抽屉 ESC 关闭</h1><button type="button" @click="record(true)">重新打开</button><button type="button" class="open-editor" @click="editorOpen = true">打开文件编辑器</button><span class="state">{{ open ? '抽屉打开中' : '抽屉已关闭' }}</span><span class="events">{{ events.join(' → ') }}</span><span class="editor-state">{{ editorOpen ? '编辑器打开中' : '编辑器已关闭' }}</span></header>
      <TerminalDrawer :open="open" title="容器终端：edo-multi-image-demo-api-1" path="/api/v1/terminals/docker/preview-host/containers/preview-container/ws?project_id=project-a" permission="terminal.open" @update:open="record" />
      <p class="hint">按 ESC 关闭抽屉；焦点在终端或文件面板时同样关闭；编辑器弹窗打开时 ESC 只关闭编辑器。</p>
      <Modal v-model:open="editorOpen" title="编辑文件：/etc/nginx/nginx.conf" :footer="null"><p>模拟终端内文件编辑器</p></Modal>
    </main>
  </ConfigProvider>
</template>

<style scoped>
.esc-preview { display: grid; gap: 12px; padding: 24px; min-height: 100vh; background: var(--edo-bg); }
header { display: flex; align-items: center; gap: 14px; }
header h1 { margin: 0; font-size: 16px; }
header button { height: 30px; padding: 0 12px; border: 1px solid var(--edo-border); border-radius: 8px; background: var(--edo-surface); color: var(--edo-text); cursor: pointer; }
.state, .events, .editor-state, .hint { color: var(--edo-muted); font-size: 12px; }
</style>
