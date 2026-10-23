<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ count: number | null; label: string; display: string }>()
const router = useRouter(), auth = useAuthStore()
const readable = computed(() => auth.canAny(['task.read']))
const available = computed(() => props.count !== null && props.count > 0)
const tasks = ref<Array<{ id: string; kind: string; error_message: string }>>([])
const loading = ref(false), error = ref(''), loaded = ref(false)
let revision = 0, loadedAt = 0, controller: AbortController | undefined, pending: Promise<void> | undefined
function load(): Promise<void> {
  if (loading.value && pending) return pending
  if (!readable.value || !available.value || (loaded.value && Date.now() - loadedAt < 30000)) return Promise.resolve()
  const request = ++revision
  controller?.abort(); controller = new AbortController()
  const signal = controller.signal
  loading.value = true; error.value = ''
  pending = (async () => {
    try {
      const { data } = await client.get<{ tasks: typeof tasks.value }>('/tasks', { params: { status: 'failed', limit: 3 }, signal })
      if (request !== revision || signal.aborted) return
      tasks.value = data.tasks; loaded.value = true; loadedAt = Date.now()
    } catch (cause) {
      if (request === revision && !signal.aborted) error.value = apiErrorMessage(cause)
    } finally { if (request === revision) { loading.value = false; pending = undefined } }
  })()
  return pending
}
async function openDetails() {
  if (!available.value || !readable.value) return
  await load()
  if (!available.value || !readable.value) return
  void router.push({ path: '/operations', query: { section: 'tasks', status: 'failed', ...(tasks.value[0] ? { task: tasks.value[0].id } : {}) } })
}
watch([() => props.count, () => auth.user?.id, readable], () => {
  revision++; controller?.abort(); tasks.value = []; loaded.value = false; loadedAt = 0; pending = undefined; loading.value = false; error.value = ''
})
onBeforeUnmount(() => { revision++; controller?.abort() })
</script>

<template>
  <a-popover placement="topLeft" :trigger="['hover', 'focus']" @open-change="(open: boolean) => { if (open) void load() }">
    <template #content>
      <div class="failed-tasks-preview">
        <strong>最近失败任务</strong>
        <p v-if="!readable">没有查看任务的权限</p>
        <p v-else-if="!available">暂无失败任务</p>
        <a-spin v-else-if="loading" size="small" />
        <template v-else-if="error"><p>{{ error }}</p><a-button size="small" @click="load">重试</a-button></template>
        <p v-else-if="loaded && !tasks.length">暂无可查看的失败任务</p>
        <template v-else><router-link v-for="task in tasks" :key="task.id" :to="{path:'/operations',query:{section:'tasks',status:'failed',task:task.id}}"><small>{{ task.kind }}</small><span>{{ task.error_message || '任务执行失败，点击查看详情' }}</span></router-link></template>
      </div>
    </template>
    <div>
      <dt>{{ label }}</dt>
      <dd><button v-if="available && readable" type="button" class="failed-tasks-value" :aria-label="`${label} ${display}，查看失败详情`" @click="openDetails">{{ display }}</button><span v-else>{{ display }}</span></dd>
      <slot />
    </div>
  </a-popover>
</template>

<style scoped>
dt{color:var(--edo-muted);font-size:12px}dd{margin:4px 0 2px;font-weight:650;font-size:15px;line-height:21px;color:var(--edo-danger,#e94f5f)}.failed-tasks-value{padding:0;border:0;background:none;color:inherit;font:inherit;cursor:pointer}.failed-tasks-value:hover{text-decoration:underline}.failed-tasks-value:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px}.failed-tasks-preview{width:min(310px,75vw)}.failed-tasks-preview>strong{display:block;margin-bottom:8px}.failed-tasks-preview p{margin:8px 0;color:var(--edo-muted)}.failed-tasks-preview a{display:block;padding:8px 0;border-top:1px solid var(--edo-border)}.failed-tasks-preview small{display:block;color:var(--edo-muted)}.failed-tasks-preview span{display:-webkit-box;overflow:hidden;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow-wrap:anywhere}
</style>
