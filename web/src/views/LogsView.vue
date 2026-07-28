<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { RefreshCw, Search } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'

interface ExecutionLog {
  id: number
  pipeline_run_id: string
  application_name: string
  stage: string
  level: string
  message: string
  created_at: string
}
interface LogResponse { items: ExecutionLog[]; next_before_id: number; has_more: boolean }

const items = ref<ExecutionLog[]>([])
const queryInput = ref('')
const query = ref('')
const level = ref('')
const nextBeforeID = ref(0)
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)

const levelLabels: Record<string, string> = { info: '信息', output: '输出', warning: '警告', error: '错误', success: '成功' }

async function load(append = false) {
  append ? loadingMore.value = true : loading.value = true
  try {
    const response = await client.get<LogResponse>('/logs', { params: { limit: 100, level: level.value || undefined, query: query.value || undefined, before_id: append && nextBeforeID.value ? nextBeforeID.value : undefined } })
    items.value = append ? [...items.value, ...response.data.items] : response.data.items
    nextBeforeID.value = response.data.next_before_id || 0
    hasMore.value = response.data.has_more
  } catch (error) { message.error(apiErrorMessage(error)) }
  finally { loading.value = false; loadingMore.value = false }
}

function search() { query.value = queryInput.value.trim(); void load(false) }
watch(level, () => void load(false))
onMounted(() => void load(false))
</script>

<template>
  <section>
    <PageToolbar description="集中查看流水线检出、构建和发布日志；审计操作仍在“审计日志”中单独保留。"><a-button :loading="loading" @click="load(false)"><RefreshCw :size="15" />刷新</a-button></PageToolbar>
    <div class="log-filter vben-card">
      <a-input v-model:value="queryInput" allow-clear placeholder="应用、运行 ID、阶段或日志内容" @press-enter="search"><template #prefix><Search :size="15" /></template></a-input>
      <a-select v-model:value="level" :options="[{ label: '全部级别', value: '' }, ...Object.entries(levelLabels).map(([value,label]) => ({ value,label }))]" />
      <a-button type="primary" @click="search">查询</a-button>
    </div>
    <a-spin :spinning="loading">
      <div class="log-list vben-card">
        <a-empty v-if="!items.length" description="暂无符合条件的流水线日志" />
        <article v-for="item in items" :key="item.id" class="log-item" :class="`level-${item.level}`">
          <header><a-tag :color="item.level === 'error' ? 'error' : item.level === 'warning' ? 'warning' : item.level === 'success' ? 'success' : 'blue'">{{ levelLabels[item.level] || item.level }}</a-tag><strong>{{ item.application_name || '未知应用' }}</strong><span>{{ item.stage || 'pipeline' }}</span><time>{{ new Date(item.created_at).toLocaleString('zh-CN', { hour12: false }) }}</time></header>
          <pre>{{ item.message }}</pre>
          <footer><span>运行 ID</span><code>{{ item.pipeline_run_id }}</code></footer>
        </article>
        <div v-if="hasMore" class="load-more"><a-button :loading="loadingMore" @click="load(true)">加载更早日志</a-button></div>
      </div>
    </a-spin>
  </section>
</template>

<style scoped>
.log-filter { display: grid; grid-template-columns: minmax(240px,1fr) 160px auto; gap: 10px; margin-bottom: 14px; padding: 12px; }.log-list { overflow: hidden; }.log-list > .ant-empty { padding: 70px 20px; }.log-item { padding: 16px 18px; border-left: 3px solid #79a8ff; border-bottom: 1px solid var(--zrt-border); }.log-item.level-error { border-left-color:#ef5656; }.log-item.level-warning { border-left-color:#efaa3a; }.log-item.level-success { border-left-color:#31b978; }.log-item header { display: flex; align-items: center; gap: 9px; color: var(--zrt-muted); }.log-item header strong { color: var(--zrt-text); }.log-item time { margin-left: auto; font-size: 12px; }.log-item pre { margin: 12px 0; overflow: auto; color: var(--zrt-text); white-space: pre-wrap; word-break: break-word; }.log-item footer { display: flex; gap: 8px; color: var(--zrt-muted); font-size: 12px; }.load-more { display: flex; justify-content: center; padding: 16px; } @media (max-width:700px){.log-filter{grid-template-columns:1fr}.log-item header{flex-wrap:wrap}.log-item time{width:100%;margin:0}}
</style>
