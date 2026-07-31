<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { RefreshCw, Search } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'

interface SystemLog {
  id: number
  created_at: string
  level: string
  message: string
  operation?: string
  fields?: Record<string, string>
}
interface LogResponse { items: SystemLog[]; next_before_id: number; has_more: boolean }

const { t, locale } = useI18n()
const items = ref<SystemLog[]>([])
const queryInput = ref('')
const query = ref('')
const level = ref('')
const nextBeforeID = ref(0)
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)

const levelLabels = computed<Record<string, string>>(() => ({
  debug: t('systemLogs.level.debug'),
  info: t('systemLogs.level.info'),
  warn: t('systemLogs.level.warn'),
  error: t('systemLogs.level.error'),
}))

function levelColor(value: string) {
  if (value === 'error') return 'error'
  if (value === 'warn') return 'warning'
  if (value === 'debug') return 'default'
  return 'blue'
}

function formatTime(value: string) {
  return new Date(value).toLocaleString(locale.value, { hour12: false })
}

async function load(append = false) {
  append ? loadingMore.value = true : loading.value = true
  try {
    const response = await client.get<LogResponse>('/logs', {
      params: {
        limit: 100,
        level: level.value || undefined,
        query: query.value || undefined,
        before_id: append && nextBeforeID.value ? nextBeforeID.value : undefined,
      },
    })
    items.value = append ? [...items.value, ...response.data.items] : response.data.items
    nextBeforeID.value = response.data.next_before_id || 0
    hasMore.value = response.data.has_more
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

function search() {
  query.value = queryInput.value.trim()
  void load(false)
}

watch(level, () => void load(false))
onMounted(() => void load(false))
</script>

<template>
  <section>
    <PageToolbar :description="t('systemLogs.description')">
      <a-tag color="processing">{{ t('systemLogs.currentProcess') }}</a-tag>
      <a-button :loading="loading" @click="load(false)"><RefreshCw :size="15" />{{ t('common.refresh') }}</a-button>
    </PageToolbar>
    <a-alert type="info" show-icon :message="t('systemLogs.scopeHint')" class="scope-alert" />
    <div class="log-filter vben-card">
      <a-input v-model:value="queryInput" allow-clear :placeholder="t('systemLogs.searchPlaceholder')" @press-enter="search"><template #prefix><Search :size="15" /></template></a-input>
      <a-select v-model:value="level" :options="[{ label: t('systemLogs.level.all'), value: '' }, ...Object.entries(levelLabels).map(([value,label]) => ({ value,label }))]" />
      <a-button type="primary" @click="search">{{ t('common.search') }}</a-button>
    </div>
    <a-spin :spinning="loading">
      <div class="log-list vben-card">
        <a-empty v-if="!items.length" :description="t('systemLogs.empty')" />
        <article v-for="item in items" :key="item.id" class="log-item" :class="`level-${item.level}`">
          <header>
            <a-tag :color="levelColor(item.level)">{{ levelLabels[item.level] || item.level }}</a-tag>
            <strong>{{ item.operation || t('systemLogs.defaultOperation') }}</strong>
            <time>{{ formatTime(item.created_at) }}</time>
          </header>
          <pre>{{ item.message }}</pre>
          <footer v-if="item.fields && Object.keys(item.fields).length">
            <span v-for="(value, key) in item.fields" :key="key"><b>{{ key }}</b><code>{{ value }}</code></span>
          </footer>
        </article>
        <div v-if="hasMore" class="load-more"><a-button :loading="loadingMore" @click="load(true)">{{ t('systemLogs.loadEarlier') }}</a-button></div>
      </div>
    </a-spin>
  </section>
</template>

<style scoped>
.scope-alert { margin-bottom: 12px; }.log-filter { display: grid; grid-template-columns: minmax(240px,1fr) 160px auto; gap: 10px; margin-bottom: 14px; padding: 12px; }.log-list { overflow: hidden; }.log-list > .ant-empty { padding: 70px 20px; }.log-item { padding: 16px 18px; border-left: 3px solid #79a8ff; border-bottom: 1px solid var(--edo-border); }.log-item.level-error { border-left-color:#ef5656; }.log-item.level-warn { border-left-color:#efaa3a; }.log-item.level-debug { border-left-color:#9aa4b2; }.log-item header { display: flex; align-items: center; gap: 9px; color: var(--edo-muted); }.log-item header strong { color: var(--edo-text); }.log-item time { margin-left: auto; font-size: 12px; }.log-item pre { margin: 12px 0; overflow: auto; color: var(--edo-text); white-space: pre-wrap; word-break: break-word; }.log-item footer { display: flex; flex-wrap: wrap; gap: 7px; color: var(--edo-muted); font-size: 12px; }.log-item footer span { display: inline-flex; align-items: center; overflow: hidden; border: 1px solid var(--edo-border); border-radius: 5px; background: var(--edo-layout); }.log-item footer b { padding: 3px 6px; border-right: 1px solid var(--edo-border); font-weight: 500; }.log-item footer code { max-width: min(520px,70vw); padding: 3px 6px; overflow: hidden; color: var(--edo-text); text-overflow: ellipsis; white-space: nowrap; }.load-more { display: flex; justify-content: center; padding: 16px; } @media (max-width:700px){.log-filter{grid-template-columns:1fr}.log-item header{flex-wrap:wrap}.log-item time{width:100%;margin:0}}
</style>
