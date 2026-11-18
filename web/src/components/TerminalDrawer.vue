<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Folder, TerminalSquare } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { terminalActorContext, type HostTerminalState } from '@/stores/hostTerminals'
import TerminalSessionPane from '@/components/TerminalSessionPane.vue'
import TerminalFileBrowser from '@/components/TerminalFileBrowser.vue'
import SessionPanel from '@/components/SessionPanel.vue'
import SessionPopoutLink from '@/components/SessionPopoutLink.vue'

const props=withDefaults(defineProps<{open:boolean;title:string;path:string;standalone?:boolean;filesOnly?:boolean;permission?:string;sourceDeploymentID?:string}>(),{standalone:false,filesOnly:false,permission:'terminal.open'})
const emit=defineEmits<{'update:open':[value:boolean]}>()
const auth = useAuthStore(), state = ref<HostTerminalState>('connecting'), showFiles = ref(true), mobile = ref(false)
const fileBrowser = ref<{ confirmDiscard: () => Promise<boolean> }>()
const canFiles = computed(() => auth.canAny(['terminal.file.read', 'terminal.file.upload']))
const permitted = computed(() => props.filesOnly
  ? ['terminal.file.read', 'kubernetes.workload.read', 'kubernetes.workload.execute'].every(permission => auth.canAny([permission]))
  : auth.canAny([props.permission]))
const actor = () => terminalActorContext(auth.user, typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? '')
let openedContext = ''
const stateLabel = computed(() => ({ connecting: '连接中', connected: '已连接', closed: '已结束', error: '连接失败' }[state.value]))
async function close() { if (!fileBrowser.value || await fileBrowser.value.confirmDiscard()) emit('update:open', false) }
// 统一规范：终端抽屉与其它弹层一样用 ESC 关闭，且与 × 走同一条关闭逻辑（含文件草稿确认）。
// 终端自身已获得焦点时同样关闭；上层仍有弹窗（例如终端内文件编辑器）时只关闭最上层。
function topLayerIsModal() {
  return Array.from(document.querySelectorAll<HTMLElement>('.ant-modal-wrap')).some(element => getComputedStyle(element).display !== 'none')
}
function keyboard(event: KeyboardEvent) {
  if (!props.open || event.key !== 'Escape' || event.defaultPrevented) return
  if (event.ctrlKey || event.metaKey || event.altKey || event.shiftKey || topLayerIsModal()) return
  event.preventDefault()
  event.stopPropagation()
  void close()
}
function resize() { mobile.value = window.innerWidth <= 700 }
function contextChanged() { if (props.open && (openedContext !== actor() || !permitted.value)) emit('update:open', false) }
watch(() => [props.open, props.path], () => { if (props.open) { openedContext = actor(); state.value = 'connecting' } }, { immediate: true })
watch([actor, permitted], contextChanged, { flush: 'sync' })
onMounted(() => { resize(); window.addEventListener('resize', resize); window.addEventListener('storage', contextChanged); window.addEventListener('edo:projects-changed', contextChanged); window.addEventListener('keydown', keyboard, true) })
onBeforeUnmount(() => { window.removeEventListener('resize', resize); window.removeEventListener('storage', contextChanged); window.removeEventListener('edo:projects-changed', contextChanged); window.removeEventListener('keydown', keyboard, true) })
</script>

<template>
  <SessionPanel :open="open" :title="title" :standalone="standalone" placement="bottom" height="min(78vh, 850px)" class="terminal-drawer" :class="{ standalone }" :mask-closable="false" :body-style="{ padding: 0 }" @close="close">
    <template v-if="!filesOnly" #extra><span class="terminal-connection" :class="state" role="status"><i />{{ stateLabel }}</span><button v-if="canFiles" type="button" class="terminal-files-toggle" :aria-pressed="showFiles" @click="showFiles = !showFiles"><Folder :size="15" />文件</button><SessionPopoutLink v-if="!standalone" kind="terminal" :path="path" :title="title" :source-deployment-i-d="sourceDeploymentID" /></template>
    <div v-if="open" class="container-terminal-workspace" :class="{ 'files-shown': showFiles && canFiles, 'files-only': filesOnly }">
      <TerminalFileBrowser v-if="filesOnly?permitted:canFiles" v-show="filesOnly||showFiles" ref="fileBrowser" :key="path" :path="path" />
      <template v-if="!filesOnly">
        <div v-show="!mobile || !showFiles || !canFiles" class="container-terminal-pane"><TerminalSessionPane :key="path" :path="path" :permission="permission" :visible="open && (!mobile || !showFiles || !canFiles)" @state="state = $event" /></div>
        <button v-if="mobile && showFiles && canFiles" class="mobile-console-return" type="button" @click="showFiles = false"><TerminalSquare :size="16" />返回终端</button>
      </template>
    </div>
  </SessionPanel>
</template>
<style scoped>
.standalone .container-terminal-workspace{flex:1;height:auto;min-height:0}
.files-only :deep(.terminal-files){flex:1;width:auto;border:0}
.container-terminal-workspace{display:flex;height:calc(min(78vh,850px) - 58px);min-height:220px;min-width:0;position:relative}.container-terminal-pane{flex:1;min-width:0;min-height:0}.terminal-connection{display:inline-flex;align-items:center;gap:6px;margin-right:12px;font-size:12px;color:var(--edo-muted)}.terminal-connection i{width:6px;height:6px;border-radius:50%;background:var(--edo-muted)}.terminal-connection.connected i{background:var(--edo-success,#22a06b)}.terminal-connection.error i{background:var(--edo-error,#d33f57)}.terminal-files-toggle,.mobile-console-return{display:inline-flex;align-items:center;justify-content:center;gap:6px;min-height:30px;padding:4px 9px;border:1px solid var(--edo-border);border-radius:4px;background:var(--edo-surface);color:var(--edo-text);cursor:pointer}.terminal-files-toggle[aria-pressed="true"]{background:var(--edo-primary-soft);color:var(--edo-primary)}.terminal-files-toggle:focus-visible,.mobile-console-return:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.mobile-console-return{position:absolute;bottom:12px;right:12px;box-shadow:0 3px 12px #0002}@media(max-width:700px){.container-terminal-workspace{height:calc(78vh - 68px)}.terminal-connection{margin-right:6px}.terminal-files-toggle{min-height:34px}}
</style>
