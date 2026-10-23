<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Bot, Check, CircleStop, MessageSquare, Plus, RefreshCw, Send, ShieldCheck } from 'lucide-vue-next'
import {
  createAssistantConversation, getAssistantConversation, getAssistantStatus, listAssistantConversations,
  resolveAssistantAction, streamAssistantMessage, type AssistantAction, type AssistantConversation,
  type AssistantEvent, type AssistantMessage, type AssistantStatus,
} from '@/api/assistant'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import AssistantMessageContent from '@/components/AssistantMessageContent.vue'

defineProps<{ compact?: boolean }>()
const { t } = useI18n(), auth = useAuthStore()
const conversations = ref<AssistantConversation[]>([]), selected = ref(''), messages = ref<AssistantMessage[]>([]), actions = ref<AssistantAction[]>([])
const status = ref<AssistantStatus | null>(null), loading = ref(false), streaming = ref(false), pendingAction = ref(''), error = ref(''), draft = ref('')
const pendingDecision = ref('')
const tools = ref<Array<{ name: string; running: boolean }>>([]), transcript = ref<HTMLElement>(), clock = ref(Date.now())
const toolLabels: Record<string, string> = {
  project_resources: 'toolResources', project_tasks: 'toolTasks', application_deployments: 'toolDeployments', deployment_diagnostics: 'toolRuntime', deployment_logs: 'toolContainerLogs',
  kubernetes_pod_logs: 'toolPodLogs', kubernetes_diagnostics: 'toolKubeDiagnostics', platform_diagnostics: 'toolPlatform', list_applications: 'toolApplications', list_pipeline_runs: 'toolRuns',
  pipeline_failure_logs: 'toolPipelineLogs', prepare_pipeline_retry: 'toolRetry', prepare_pipeline_stop: 'toolStop', list_kubernetes_clusters: 'toolClusters',
  kubernetes_resources: 'toolKubeResources', kubernetes_resource_detail: 'toolKubeDetail', kubernetes_events: 'toolKubeEvents',
}
function toolLabel(name: string) { return t(`assistant.${toolLabels[name] || 'tool'}`) }
const canRead = computed(() => auth.canAny(['assistant.read'])), canExecute = computed(() => auth.canAny(['assistant.execute']))
const available = computed(() => status.value?.enabled && status.value.configured)
const statusLabel = computed(() => !status.value ? 'assistant.loading' : !status.value.configured ? 'assistant.unconfigured' : status.value.enabled ? 'assistant.configured' : 'assistant.disabled')
const canSend = computed(() => canRead.value && canExecute.value && available.value && Boolean(project()) && Boolean(draft.value.trim()) && !loading.value && !streaming.value && !pendingAction.value)
const safeActions = computed(() => actions.value.filter(action => ['pipeline_retry', 'pipeline_stop'].includes(action.kind)))
const unavailableHint = computed(() => !project() ? t('assistant.noProject') : !canExecute.value ? t('assistant.noExecute') : !status.value?.configured ? t('assistant.setupHint') : t('assistant.disabledHint'))
let revision = 0, disposed = false, timer = 0
const controllers = new Set<AbortController>()
function project() { return localStorage.getItem('edo.project') || '' }
function identity() { return `${auth.user?.id || ''}:${project()}` }
function requestController() { const controller = new AbortController(); controllers.add(controller); return controller }
function current(request: number, context: string) { return !disposed && revision === request && identity() === context && canRead.value }
function invalidate() {
  revision++
  for (const controller of controllers) controller.abort()
  controllers.clear()
  loading.value = false; streaming.value = false; pendingAction.value = ''; pendingDecision.value = ''; tools.value = []
}
function replaceAction(action: AssistantAction) {
  const index = actions.value.findIndex(item => item.id === action.id)
  if (index < 0) actions.value.push(action)
  else actions.value[index] = action
}
function actionStatus(action: AssistantAction) {
  if (action.status === 'pending' && Date.parse(action.expires_at) <= clock.value) return 'expired'
  return action.status
}
function actionLabel(action: AssistantAction) {
  const key: Record<string, string> = { pending: 'actionPending', confirmed: 'actionConfirmed', executing: 'actionExecuting', rejected: 'actionRejected', succeeded: 'actionSucceeded', failed: 'actionFailed', expired: 'actionExpired' }
  return t(`assistant.${key[actionStatus(action)] || 'actionUnknown'}`)
}
async function scrollMessages() { await nextTick(); transcript.value?.scrollTo({ top: transcript.value.scrollHeight, behavior: 'auto' }) }

async function load() {
  invalidate()
  const request = revision, context = identity(), controller = requestController()
  selected.value = ''; messages.value = []; actions.value = []; conversations.value = []; status.value = null; error.value = ''; draft.value = ''
  if (!canRead.value || !project()) { controllers.delete(controller); return }
  loading.value = true
  try {
    const [availability, list] = await Promise.all([getAssistantStatus(controller.signal), listAssistantConversations(controller.signal)])
    if (!current(request, context)) return
    status.value = availability; conversations.value = list
    if (list[0]) {
      const detail = await getAssistantConversation(list[0].id, controller.signal)
      if (!current(request, context)) return
      selected.value = detail.conversation.id; messages.value = detail.messages ?? []; actions.value = detail.actions ?? []
      void scrollMessages()
    }
  } catch (cause) { if (current(request, context) && !controller.signal.aborted) error.value = apiErrorMessage(cause) }
  finally { controllers.delete(controller); if (current(request, context)) loading.value = false }
}
async function selectConversation(id: string) {
  if (id === selected.value || pendingAction.value) return
  invalidate()
  const request = revision, context = identity(), controller = requestController()
  selected.value = id; messages.value = []; actions.value = []; error.value = ''; draft.value = ''; loading.value = true
  try {
    const detail = await getAssistantConversation(id, controller.signal)
    if (!current(request, context)) return
    messages.value = detail.messages ?? []; actions.value = detail.actions ?? []; void scrollMessages()
  } catch (cause) { if (current(request, context) && !controller.signal.aborted) error.value = apiErrorMessage(cause) }
  finally { controllers.delete(controller); if (current(request, context)) loading.value = false }
}
function newConversation() {
  if (!canExecute.value || pendingAction.value) return
  invalidate(); selected.value = ''; messages.value = []; actions.value = []; error.value = ''; draft.value = ''
}
async function synchronize(id: string, request: number, context: string) {
  const controller = requestController()
  try {
    const detail = await getAssistantConversation(id, controller.signal)
    if (!current(request, context) || selected.value !== id) return
    messages.value = detail.messages ?? []; actions.value = detail.actions ?? []
    conversations.value = [detail.conversation, ...conversations.value.filter(item => item.id !== id)]
    void scrollMessages()
  } catch { /* 同步失败保留已接收的消息，刷新后可重试。 */ }
  finally { controllers.delete(controller) }
}
async function send() {
  if (!canSend.value) return
  const request = revision, context = identity(), projectID = project(), controller = requestController(), content = draft.value.trim()
  streaming.value = true; error.value = ''; tools.value = []
  let id = selected.value, answer: AssistantMessage | undefined, failed = false
  try {
    if (!id) {
      const conversation = await createAssistantConversation(controller.signal)
      if (!current(request, context)) return
      conversations.value.unshift(conversation); selected.value = id = conversation.id
    }
    messages.value.push({ id: `local-user-${Date.now()}`, role: 'user', content })
    answer = { id: `local-assistant-${Date.now()}`, role: 'assistant', content: '', status: 'streaming' }
    messages.value.push(answer)
    // 使用数组内的响应式对象，流式文字才能及时更新视图。
    answer = messages.value[messages.value.length - 1]!
    const onEvent = (event: AssistantEvent) => {
      if (!current(request, context) || controller.signal.aborted || !answer) return
      if (event.type === 'delta' && typeof event.text === 'string') answer.content += event.text
      if (event.type === 'tool_start') tools.value.push({ name: event.tool_name || t('assistant.tool'), running: true })
      if (event.type === 'tool_end') {
        const activity = [...tools.value].reverse().find(item => item.name === event.tool_name && item.running)
        if (activity) activity.running = false
      }
      if (event.type === 'action_proposed' && event.action) replaceAction(event.action)
      if (event.type === 'done') {
        if (event.message) Object.assign(answer, event.message)
        answer.status = 'completed'
      }
      if (event.type === 'error') { failed = true; answer.status = 'failed'; error.value = event.error || t('assistant.requestFailed') }
      void scrollMessages()
    }
    await streamAssistantMessage(id, content, projectID, controller.signal, onEvent)
    if (!current(request, context) || controller.signal.aborted) return
    if (!failed) draft.value = ''
    await synchronize(id, request, context)
  } catch (cause) {
    if (!current(request, context) || controller.signal.aborted) return
    failed = true
    if (answer) answer.status = 'failed'
    error.value = cause instanceof Error && cause.name === 'AssistantStreamError' ? cause.message : t('assistant.requestFailed')
    if (id) await synchronize(id, request, context)
  } finally {
    controllers.delete(controller)
    if (current(request, context)) { streaming.value = false; tools.value = tools.value.filter(item => !item.running) }
  }
}
async function stop() {
  const id = selected.value
  invalidate(); error.value = t('assistant.stopped')
  if (!id) return
  const request = revision, context = identity()
  loading.value = true
  try { await synchronize(id, request, context) }
  finally { if (current(request, context)) loading.value = false }
}
async function resolveAction(action: AssistantAction, decision: 'confirm' | 'reject') {
  if (!canExecute.value || pendingAction.value || streaming.value || loading.value || actionStatus(action) !== 'pending' || !['pipeline_retry', 'pipeline_stop'].includes(action.kind)) return
  const request = revision, context = identity(), controller = requestController()
  pendingAction.value = action.id; pendingDecision.value = decision; error.value = ''
  try {
    const result = await resolveAssistantAction(action.id, decision, controller.signal)
    if (!current(request, context)) return
    replaceAction(result)
  } catch (cause) { if (current(request, context) && !controller.signal.aborted) error.value = apiErrorMessage(cause) || t('assistant.actionError') }
  finally { controllers.delete(controller); if (current(request, context)) { pendingAction.value = ''; pendingDecision.value = '' } }
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); void send() }
}
function contextChanged() { void load() }
function pageHidden() { invalidate() }
watch(() => [auth.user?.id, auth.user?.project_id, canRead.value, canExecute.value], () => void load(), { immediate: true })
onMounted(() => {
  timer = window.setInterval(() => { clock.value = Date.now() }, 1000)
  window.addEventListener('storage', contextChanged)
  window.addEventListener('edo:projects-changed', contextChanged)
  window.addEventListener('pagehide', pageHidden)
})
onBeforeUnmount(() => {
  disposed = true; invalidate(); window.clearInterval(timer)
  window.removeEventListener('storage', contextChanged); window.removeEventListener('edo:projects-changed', contextChanged); window.removeEventListener('pagehide', pageHidden)
})
</script>

<template>
  <section class="assistant-panel" :class="{ compact }">
    <a-alert v-if="!canRead" type="warning" :message="t('assistant.noRead')" show-icon />
    <template v-else>
      <div class="assistant-toolbar"><div class="assistant-scope"><ShieldCheck :size="16" /><span>{{ t('assistant.description') }}</span></div><div class="assistant-toolbar-actions"><a-tag :color="available ? 'success' : 'default'">{{ t(statusLabel) }}</a-tag><a-button :disabled="loading || streaming || !!pendingAction" :aria-label="t('assistant.refresh')" @click="load"><RefreshCw :size="15" /></a-button><a-button v-if="canExecute" :disabled="loading || !!pendingAction" @click="newConversation"><Plus :size="15" />{{ t('assistant.newConversation') }}</a-button></div></div>
      <a-alert v-if="status && (!available || !canExecute) || !project()" type="info" :message="unavailableHint" show-icon><template v-if="auth.canAny(['config.read']) && !available" #action><RouterLink to="/settings?section=assistant">{{ t('assistant.settings') }}</RouterLink></template></a-alert>
      <div class="assistant-workspace">
        <aside class="assistant-conversations"><label>{{ t('assistant.conversations') }}</label><a-select :value="selected || undefined" :placeholder="t('assistant.emptyConversations')" :aria-label="t('assistant.conversations')" :disabled="loading || !!pendingAction" :options="conversations.map(item => ({ value: item.id, label: item.title || t('assistant.untitled') }))" @change="(value: unknown) => selectConversation(String(value))" /></aside>
        <div class="assistant-chat">
          <div ref="transcript" class="assistant-transcript" role="log" :aria-label="t('assistant.title')" :aria-busy="loading || streaming">
            <a-spin v-if="loading" class="assistant-loading" />
            <div v-else-if="!messages.length" class="assistant-empty"><span class="assistant-empty-icon"><Bot :size="28" /></span><h3>{{ t('assistant.empty') }}</h3><p>{{ t('assistant.emptyHint') }}</p><div><button v-for="prompt in ['promptDiagnose', 'promptLogs', 'promptStatus']" :key="prompt" type="button" :disabled="!available || !canExecute" @click="draft = t(`assistant.${prompt}`)"><MessageSquare :size="15" />{{ t(`assistant.${prompt}`) }}</button></div></div>
            <article v-for="item in messages" :key="item.id" class="assistant-message" :class="{ 'is-user': item.role === 'user' }"><div class="assistant-message-role"><Bot v-if="item.role !== 'user'" :size="16" /><strong>{{ t(item.role === 'user' ? 'assistant.user' : item.role === 'tool' ? 'assistant.tool' : 'assistant.assistant') }}</strong><span v-if="item.status === 'streaming'">{{ t('assistant.generating') }}</span></div><div class="assistant-message-content"><AssistantMessageContent v-if="item.role === 'assistant' && item.status !== 'restricted'" :content="item.content" /><template v-else>{{ item.content }}</template></div></article>
            <div v-if="tools.length" class="assistant-tool-activity"><div v-for="(tool, index) in tools" :key="index"><RefreshCw v-if="tool.running" :size="14" /><Check v-else :size="14" /><span>{{ t(tool.running ? 'assistant.reading' : 'assistant.readDone') }} · {{ toolLabel(tool.name) }}</span></div></div>
            <article v-for="action in safeActions" :key="action.id" class="assistant-action"><div class="assistant-action-head"><ShieldCheck :size="17" /><strong>{{ action.title }}</strong><a-tag>{{ actionLabel(action) }}</a-tag></div><div class="assistant-action-target">{{ t('assistant.target') }}：<strong>{{ action.target_name }}</strong></div><p v-if="action.summary">{{ action.summary }}</p><div class="assistant-action-risk">{{ t(action.kind === 'pipeline_stop' ? 'assistant.stopRisk' : 'assistant.retryRisk') }}</div><p v-if="action.result?.message">{{ action.result.message }}</p><div class="assistant-action-buttons"><template v-if="actionStatus(action) === 'pending' && canExecute"><a-button :type="action.kind === 'pipeline_stop' ? 'default' : 'primary'" :danger="action.kind === 'pipeline_stop'" :loading="pendingAction === action.id && pendingDecision === 'confirm'" :disabled="!!pendingAction || streaming || loading" @click="resolveAction(action, 'confirm')">{{ t('assistant.confirm') }}</a-button><a-button :loading="pendingAction === action.id && pendingDecision === 'reject'" :disabled="!!pendingAction || streaming || loading" @click="resolveAction(action, 'reject')">{{ t('assistant.reject') }}</a-button></template><RouterLink v-if="action.result?.pipeline_run_id && auth.canAny(['pipeline_run.read'])" :to="{ path: '/release-plans', query: { view: 'runs', run: action.result.pipeline_run_id } }">{{ t('assistant.viewRun') }}</RouterLink></div></article>
          </div>
          <div class="assistant-composer"><a-alert v-if="error" type="warning" :message="error" show-icon closable @close="error = ''" /><a-textarea v-model:value="draft" :aria-label="t('assistant.placeholder')" :placeholder="t('assistant.placeholder')" :auto-size="{ minRows: 3, maxRows: 8 }" :maxlength="16000" :disabled="!available || !canExecute || loading || streaming || !!pendingAction" @keydown="keydown" /><div class="assistant-composer-actions"><span>{{ streaming ? t('assistant.generating') : '' }}</span><a-button v-if="streaming" @click="stop"><CircleStop :size="15" />{{ t('assistant.stop') }}</a-button><a-button v-else type="primary" :disabled="!canSend" @click="send"><Send :size="15" />{{ t('assistant.send') }}</a-button></div></div>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.assistant-panel{display:flex;flex-direction:column;gap:12px;min-height:520px;height:calc(100dvh - 150px);max-height:960px;color:var(--edo-text)}.assistant-toolbar{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.assistant-scope{display:flex;align-items:center;gap:7px;color:var(--edo-muted)}.assistant-scope>svg{color:var(--edo-primary);flex-shrink:0}.assistant-toolbar-actions{display:flex;align-items:center;gap:8px}.assistant-toolbar-actions .ant-tag{margin:0}.assistant-workspace{display:flex;flex:1;min-height:0;flex-direction:column;border:1px solid var(--edo-border);border-radius:10px;background:var(--edo-surface);box-shadow:var(--edo-shadow);overflow:hidden}.assistant-conversations{display:flex;align-items:center;gap:12px;flex-shrink:0;padding:12px 18px;border-bottom:1px solid var(--edo-border);background:var(--edo-surface-soft)}.assistant-conversations label{color:var(--edo-muted);white-space:nowrap}.assistant-conversations .ant-select{width:320px;max-width:100%}.assistant-chat{display:flex;flex:1;min-height:0;flex-direction:column}.assistant-transcript{flex:1;min-height:0;overflow-y:auto;padding:22px;overscroll-behavior:contain}.assistant-loading{display:block;margin:60px auto}.assistant-empty{max-width:580px;margin:38px auto;text-align:center}.assistant-empty-icon{display:inline-flex;padding:14px;border-radius:14px;background:var(--edo-primary-soft);color:var(--edo-primary)}.assistant-empty h3{margin:16px 0 8px;font-size:18px}.assistant-empty p{color:var(--edo-muted);line-height:1.8}.assistant-empty>div{display:flex;gap:10px;justify-content:center;flex-wrap:wrap;margin-top:20px}.assistant-empty button{display:flex;gap:8px;align-items:center;padding:10px 12px;border:1px solid var(--edo-border);border-radius:8px;background:var(--edo-surface);color:var(--edo-text);cursor:pointer;text-align:left}.assistant-empty button:disabled{cursor:not-allowed;opacity:.55}.assistant-message{max-width:900px;margin:0 auto 20px}.assistant-message-role{display:flex;align-items:center;gap:7px;margin-bottom:8px}.assistant-message-role>svg{color:var(--edo-primary)}.assistant-message-role span{font-size:12px;color:var(--edo-muted)}.assistant-message-content{white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.8}.is-user .assistant-message-content{padding:12px 15px;border:1px solid var(--edo-border);border-radius:9px;background:var(--edo-surface-soft)}.assistant-tool-activity{max-width:900px;margin:0 auto 18px;color:var(--edo-muted);font-size:13px}.assistant-tool-activity>div{display:flex;align-items:center;gap:7px;margin:6px 0}.assistant-action{max-width:900px;margin:0 auto 16px;padding:16px;border:1px solid var(--edo-border);border-radius:9px;background:var(--edo-surface-soft)}.assistant-action-head{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.assistant-action-head>svg{color:var(--edo-primary)}.assistant-action-head .ant-tag{margin-left:auto}.assistant-action-target{margin:12px 0 8px}.assistant-action p{white-space:pre-wrap;overflow-wrap:anywhere;margin:8px 0}.assistant-action-risk{color:var(--edo-muted);line-height:1.7}.assistant-action-buttons{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-top:14px}.assistant-action-buttons:empty{display:none}.assistant-composer{flex-shrink:0;padding:14px 18px 12px;border-top:1px solid var(--edo-border);background:var(--edo-surface)}.assistant-composer .ant-alert{margin-bottom:10px}.assistant-composer-actions{display:flex;justify-content:flex-end;align-items:center;gap:10px;margin-top:10px}.assistant-composer-actions>span{margin-right:auto;color:var(--edo-muted);font-size:13px}.compact{height:100%;min-height:0;max-height:none}.compact .assistant-transcript{padding:18px}.compact .assistant-scope{font-size:13px}.compact .assistant-toolbar-actions{margin-left:auto}@media(max-width:600px){.assistant-panel{min-height:540px;height:calc(100dvh - 165px)}.compact{height:100%;min-height:0}.assistant-transcript{padding:16px}.assistant-conversations{padding:10px 12px}.assistant-conversations .ant-select{flex:1;width:0}.assistant-composer{padding:12px}.assistant-toolbar-actions{margin-left:auto}.assistant-empty{margin:18px auto}.assistant-empty>div{flex-direction:column}.assistant-action{padding:12px}}
</style>
