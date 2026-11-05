<script setup>
import { ref } from 'vue'
import { Button, ConfigProvider, Drawer, Modal, theme } from 'ant-design-vue'
import { SaveOutlined } from '@ant-design/icons-vue'
import { ArrowRight, Plus, RefreshCw } from 'lucide-vue-next'
import RuntimeBrandIcon from '/src/components/RuntimeBrandIcon.vue'

const dark = ref(new URLSearchParams(location.search).get('theme') === 'dark')
const loading = ref(true)
const modalOpen = ref(false), drawerOpen = ref(false)
const sizes = ['small', 'middle', 'large']
document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'
function toggleTheme() {
  dark.value = !dark.value
  document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'
}
</script>

<template>
  <ConfigProvider :theme="{ algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm }">
    <main class="button-preview">
      <header><h1>EDO 按钮布局回归</h1><Button @click="toggleTheme">切换主题</Button><Button @click="loading = !loading">切换加载状态</Button><Button @click="modalOpen = true">打开弹窗</Button><Button @click="drawerOpen = true">打开抽屉</Button></header>
      <section v-for="size in sizes" :key="size" :data-size="size">
        <h2>{{ size }}</h2>
        <div class="button-cases">
          <Button :size="size" data-case="leading-svg"><Plus :size="16" />新建主机分组</Button>
          <Button :size="size" data-case="trailing-svg">下一步<ArrowRight :size="16" /></Button>
          <Button :size="size" data-case="brand"><RuntimeBrandIcon kind="docker" />运行资源</Button>
          <Button :size="size" type="primary" data-case="primary"><Plus :size="16" />添加主机</Button>
          <Button :size="size" type="link" data-case="link"><ArrowRight :size="16" />查看详情</Button>
          <Button :size="size" type="text" data-case="text"><RefreshCw :size="16" />刷新列表</Button>
          <Button :size="size" danger data-case="danger"><Plus :size="16" />危险操作样式</Button>
          <Button :size="size" disabled data-case="disabled"><Plus :size="16" />不可用操作</Button>
          <Button :size="size" data-case="slot-svg"><template #icon><Plus :size="16" /></template>添加配置</Button>
          <Button :size="size" data-case="ant-icon"><template #icon><SaveOutlined /></template>保存配置</Button>
          <Button :size="size" :loading="loading" data-case="loading-text">加载数据</Button>
          <Button :size="size" :loading="loading" data-case="loading-slot"><template #icon><RefreshCw :size="16" /></template>刷新数据</Button>
          <Button :size="size" :loading="loading" data-case="loading-inline"><RefreshCw :size="16" />读取资源</Button>
          <Button :size="size" data-case="icon-only" aria-label="刷新"><template #icon><RefreshCw :size="16" /></template></Button>
          <Button :size="size" :loading="loading" data-case="loading-icon-only" aria-label="加载"><template #icon><RefreshCw :size="16" /></template></Button>
          <Button :size="size" data-case="plain-text">确认操作</Button>
          <Button :size="size" href="#button-anchor" data-case="anchor"><ArrowRight :size="16" />按钮链接</Button>
        </div>
      </section>
      <section class="resource-row-actions"><Button size="small" data-case="compact"><RefreshCw :size="14" />表格操作</Button><Button class="resource-more-button" data-case="compact-icon" aria-label="更多操作"><Plus :size="14" /></Button></section>
      <footer id="button-anchor">本地样式夹具，不连接业务接口。</footer>
    </main>
    <Modal v-model:open="modalOpen" title="弹窗按钮回归">
      <Button data-case="modal-body"><RefreshCw :size="16" />刷新数据</Button>
      <template #footer><Button @click="modalOpen = false">取消</Button><Button data-case="modal-footer" type="primary"><Plus :size="16" />确认添加</Button></template>
    </Modal>
    <Drawer v-model:open="drawerOpen" title="抽屉按钮回归">
      <Button data-case="drawer-body"><RefreshCw :size="16" />刷新数据</Button>
      <template #footer><Button data-case="drawer-footer" type="primary"><Plus :size="16" />保存配置</Button></template>
    </Drawer>
  </ConfigProvider>
</template>

<style scoped>
.button-preview{max-width:1200px;margin:auto;padding:20px;color:var(--edo-text)}
header{display:flex;align-items:center;flex-wrap:wrap;gap:12px}h1{font-size:20px}h2{font-size:15px}
section{padding:12px 0;border-bottom:1px solid var(--edo-border)}.button-cases{display:flex;flex-wrap:wrap;align-items:center;gap:12px}
footer{padding:20px 0;color:var(--edo-muted)}
</style>
