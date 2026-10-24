<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Bot, MessageCircle, Minus, Settings } from 'lucide-vue-next'
import { getAssistantStatus, type AssistantStatus } from '@/api/assistant'
import { useAuthStore } from '@/stores/auth'

const AssistantPanel = defineAsyncComponent(() => import('@/components/AssistantPanel.vue'))
const auth = useAuthStore(), route = useRoute(), router = useRouter(), { t } = useI18n()
const status = ref<AssistantStatus | null>(null), checked = ref(false), open = ref(false), initialized = ref(false)
const launcher = ref<HTMLButtonElement>(), dialog = ref<HTMLElement>()
const canRead = computed(() => Boolean(auth.user) && auth.canAny(['assistant.read']))
const visible = computed(() => canRead.value && status.value?.enabled === true)
let controller: AbortController | undefined, revision = 0, disposed = false, timer = 0

function context() { return `${auth.user?.id || ''}:${localStorage.getItem('edo.project') || ''}` }
function reset() { open.value = false; initialized.value = false; status.value = null }
async function refresh() {
  controller?.abort()
  const request = ++revision, identity = context()
  if (!canRead.value) { reset(); checked.value = true; return }
  controller = new AbortController()
  try {
    const result = await getAssistantStatus(controller.signal)
    if (disposed || request !== revision || identity !== context() || !canRead.value) return
    status.value = result
    if (!result.enabled) reset()
  } catch {
    // 状态未能确认时收起入口，下次聚焦或定时校准时重新检查。
    if (!disposed && request === revision) reset()
  } finally { if (!disposed && request === revision) checked.value = true }
}
async function show() {
  if (!visible.value) return
  initialized.value = true; open.value = true
  await nextTick()
  if (open.value) dialog.value?.focus({ preventScroll: true })
}
async function minimize() {
  open.value = false
  await nextTick()
  if (!disposed) launcher.value?.focus({ preventScroll: true })
}
function changeContext() { reset(); checked.value = false; void refresh() }
function settingsChanged() { reset(); void refresh() }
function focus() { if (document.visibilityState !== 'hidden') void refresh() }
function storage(event: StorageEvent) { if (event.key === 'edo.project' || event.key === null) changeContext() }

watch(() => [auth.user?.id, auth.user?.project_id, canRead.value], changeContext, { immediate: true })
watch(() => [route.query.assistant, visible.value, checked.value], () => {
  if (route.query.assistant !== 'open' || !checked.value) return
  if (visible.value) void show()
  const query = { ...route.query }; delete query.assistant
  void router.replace({ path: route.path, query, hash: route.hash })
}, { immediate: true })
onMounted(() => {
  window.addEventListener('edo:assistant-settings-changed', settingsChanged)
  window.addEventListener('edo:projects-changed', changeContext)
  window.addEventListener('storage', storage)
  window.addEventListener('focus', focus)
  document.addEventListener('visibilitychange', focus)
  timer = window.setInterval(focus, 60_000)
})
onBeforeUnmount(() => {
  disposed = true; revision++; controller?.abort(); window.clearInterval(timer)
  window.removeEventListener('edo:assistant-settings-changed', settingsChanged)
  window.removeEventListener('edo:projects-changed', changeContext)
  window.removeEventListener('storage', storage)
  window.removeEventListener('focus', focus)
  document.removeEventListener('visibilitychange', focus)
})
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="floating-assistant">
      <section v-if="initialized" v-show="open" id="edo-assistant-dialog" ref="dialog" class="assistant-dialog" role="dialog" aria-modal="false" :aria-label="t('assistant.title')" tabindex="-1" @keydown.esc.stop.prevent="minimize">
        <header class="assistant-dialog-header">
          <span class="assistant-dialog-icon"><Bot :size="21" /></span><strong>{{ t('assistant.title') }}</strong>
          <RouterLink v-if="auth.canAny(['config.read'])" class="assistant-window-action" to="/settings?section=assistant" :aria-label="t('assistant.settings')" :title="t('assistant.settings')" @click="minimize"><Settings :size="18" /></RouterLink>
          <button class="assistant-window-action" type="button" :aria-label="t('assistant.minimize')" :title="t('assistant.minimize')" @click="minimize"><Minus :size="20" /></button>
        </header>
        <p class="assistant-development-notice" role="note">{{ t('assistant.developmentNotice') }}</p>
        <div class="assistant-dialog-content"><AssistantPanel compact /></div>
      </section>
      <button ref="launcher" class="assistant-bubble" type="button" :aria-label="t(open ? 'assistant.minimize' : 'assistant.open')" :aria-expanded="open" :aria-controls="initialized ? 'edo-assistant-dialog' : undefined" :title="t(open ? 'assistant.minimize' : 'assistant.open')" @click="open ? minimize() : show()"><MessageCircle v-if="!open" :size="26" /><Minus v-else :size="26" /></button>
    </div>
  </Teleport>
</template>

<style scoped>
.floating-assistant{position:fixed;right:24px;bottom:max(24px,env(safe-area-inset-bottom));z-index:950;color:var(--edo-text)}
.assistant-bubble{display:flex;align-items:center;justify-content:center;gap:9px;min-width:58px;width:58px;height:58px;padding:0;border:1px solid rgba(255,255,255,.2);border-radius:30px;background:var(--edo-primary);color:#fff;box-shadow:0 6px 24px rgba(30,55,130,.24);cursor:pointer;transition:box-shadow .2s,transform .2s}
.assistant-bubble:hover{box-shadow:0 8px 28px rgba(30,55,130,.35);transform:translateY(-2px)}
.assistant-dialog{position:absolute;right:0;bottom:74px;display:flex;flex-direction:column;width:min(600px,calc(100vw - 48px));height:min(680px,calc(100dvh - 132px));border:1px solid var(--edo-border);border-radius:16px;background:var(--edo-surface);box-shadow:0 12px 48px rgba(15,23,42,.18);overflow:hidden}
.assistant-dialog-header{display:flex;align-items:center;gap:10px;flex-shrink:0;min-height:60px;padding:12px 16px;border-bottom:1px solid var(--edo-border);background:var(--edo-surface-soft)}
.assistant-dialog-icon{display:flex;align-items:center;justify-content:center;width:34px;height:34px;border-radius:10px;color:var(--edo-primary);background:var(--edo-primary-soft)}
.assistant-dialog-header>strong{flex:1;font-size:15px}
.assistant-development-notice{flex-shrink:0;margin:0;padding:10px 14px;border-bottom:1px solid var(--edo-border);background:var(--edo-surface-soft);color:var(--edo-muted);font-size:12px;line-height:1.6}
.assistant-window-action{display:flex;align-items:center;justify-content:center;width:32px;height:32px;border:0;border-radius:8px;background:transparent;color:var(--edo-muted);cursor:pointer}
.assistant-window-action:hover{background:var(--edo-primary-soft);color:var(--edo-primary)}
.assistant-bubble:focus-visible,.assistant-window-action:focus-visible{outline:3px solid var(--edo-primary);outline-offset:3px}
.assistant-dialog:focus-visible{outline:none}
.assistant-dialog-content{flex:1;min-height:0;padding:14px;overflow:hidden}
@media(max-width:600px){.floating-assistant{right:12px;bottom:max(12px,env(safe-area-inset-bottom))}.assistant-bubble{height:54px;width:54px;min-width:54px;padding:0}.assistant-dialog{width:calc(100vw - 24px);height:min(720px,calc(100dvh - 94px));bottom:66px;border-radius:14px}.assistant-dialog-header{min-height:54px;padding:10px 12px}.assistant-dialog-content{padding:10px}}
@media(prefers-reduced-motion:reduce){.assistant-bubble{transition:none}.assistant-bubble:hover{transform:none}}
</style>
