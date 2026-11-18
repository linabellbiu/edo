<script setup lang="ts">
import { computed, ref } from 'vue'
import { ConfigProvider, theme } from 'ant-design-vue'
import { FileText, Folder, Settings2, TerminalSquare } from 'lucide-vue-next'
import SessionAction from '/src/components/SessionAction.vue'

const query = new URLSearchParams(location.search)
const dark = ref(query.get('theme') === 'dark')
if (typeof localStorage !== 'undefined') localStorage.setItem('edo.project', 'project-a')
document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'
const opened = ref('')

const logPath = '/api/v1/kubernetes/clusters/preview-cluster/namespaces/preview/pods/preview-web/containers/app/logs/ws?project_id=project-a'
const terminalPath = '/api/v1/terminals/kubernetes/preview-cluster/namespaces/preview/pods/preview-web/containers/app/ws?project_id=project-a'
const originalOpen = window.open.bind(window)
window.open = (url?: string | URL, target?: string, features?: string) => {
  opened.value = String(url)
  return { focus() {} } as unknown as Window
}

const antTheme = computed(() => ({
  token: {
    colorPrimary: '#4f6ef7', borderRadius: 6, borderRadiusLG: 8,
    colorBgLayout: dark.value ? '#0f1014' : '#f5f6f8',
    colorBgContainer: dark.value ? '#17181d' : '#ffffff',
    colorBorderSecondary: dark.value ? '#292b33' : '#e9eaee',
  },
  algorithm: dark.value ? theme.darkAlgorithm : theme.defaultAlgorithm,
}))
function toggleTheme() {
  dark.value = !dark.value
  document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'
}
</script>

<template>
  <ConfigProvider :theme="antTheme">
    <main class="action-preview">
      <header><h1>日志与终端入口：角标与 Ctrl+点击</h1><button type="button" @click="toggleTheme">切换主题</button><span class="opened">{{ opened ? '最近动作：' + opened : '尚未触发任何动作' }}</span></header>

      <section class="preview-table">
        <div class="preview-head"><span>目标</span><span>操作</span></div>
        <div class="preview-row">
          <span>preview-web / app</span>
          <div class="preview-actions">
            <SessionAction kind="container-log" :path="logPath" title="preview-web / app"><a-button size="small" @click="opened = '当前页抽屉：日志'"><FileText :size="14" />日志</a-button></SessionAction>
            <SessionAction kind="terminal" :path="terminalPath" title="preview-web / app"><a-button size="small" @click="opened = '当前页抽屉：终端'"><TerminalSquare :size="14" />Exec</a-button></SessionAction>
          </div>
        </div>
        <div class="preview-row">
          <span>preview-web / app（禁用）</span>
          <div class="preview-actions">
            <SessionAction kind="container-log" :path="logPath" title="preview-web / app" disabled><a-button size="small" disabled><FileText :size="14" />日志</a-button></SessionAction>
            <SessionAction kind="terminal" :path="terminalPath" title="preview-web / app" disabled><a-button size="small" disabled><TerminalSquare :size="14" />Exec</a-button></SessionAction>
          </div>
        </div>
      </section>

      <section class="preview-variants">
        <h2>按钮尺寸与类型</h2>
        <div class="variant-row">
          <SessionAction kind="container-log" :path="logPath" title="默认按钮"><a-button @click="opened = '当前页抽屉：日志'"><FileText :size="16" />查看日志</a-button></SessionAction>
          <SessionAction kind="container-log" :path="logPath" title="链接按钮"><a-button type="link" @click="opened = '当前页抽屉：日志'"><FileText :size="15" />查看日志</a-button></SessionAction>
          <SessionAction kind="terminal" :path="terminalPath" title="小按钮终端"><a-button size="small"><TerminalSquare :size="14" />终端</a-button></SessionAction>
          <SessionAction kind="container-log" :path="logPath" title="主按钮"><a-button type="primary">打开日志</a-button></SessionAction>
          <SessionAction kind="container-log" :path="logPath" title="无图标"><a-button size="small">日志</a-button></SessionAction>
        </div>
      </section>

      <section class="preview-variants">
        <h2>普通按钮并排（不是单一日志入口）</h2>
        <div class="variant-row">
          <a-button size="small"><Settings2 :size="14" />配置</a-button>
          <a-button size="small"><Folder :size="14" />文件</a-button>
        </div>
      </section>
    </main>
  </ConfigProvider>
</template>

<style scoped>
.action-preview { display: grid; gap: 18px; min-height: 100vh; padding: 24px; background: var(--edo-bg); }
header { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
header h1 { margin: 0; font-size: 16px; }
header button { height: 30px; padding: 0 12px; border: 1px solid var(--edo-border); border-radius: 8px; background: var(--edo-surface); color: var(--edo-text); cursor: pointer; }
.opened { color: var(--edo-muted); font-size: 12px; }
.preview-table { max-width: 720px; border: 1px solid var(--edo-border); border-radius: 12px; background: var(--edo-surface); overflow: hidden; }
.preview-head, .preview-row { display: grid; grid-template-columns: 1fr 320px; align-items: center; gap: 16px; padding: 10px 16px; }
.preview-head { border-bottom: 1px solid var(--edo-border); color: var(--edo-muted); font-size: 12px; font-weight: 600; }
.preview-row + .preview-row { border-top: 1px solid var(--edo-border); }
.preview-actions { display: flex; align-items: center; gap: 10px; }
.preview-variants h2 { margin: 0 0 8px; color: var(--edo-muted); font-size: 12px; font-weight: 500; }
.variant-row { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 14px; border: 1px solid var(--edo-border); border-radius: 12px; background: var(--edo-surface); }
</style>
