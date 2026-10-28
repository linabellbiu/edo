<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Folder, TerminalSquare } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { terminalActorContext, type HostTerminalState } from '@/stores/hostTerminals'
import TerminalSessionPane from '@/components/TerminalSessionPane.vue'
import TerminalFileBrowser from '@/components/TerminalFileBrowser.vue'

const props=defineProps<{open:boolean;title:string;path:string}>()
const emit=defineEmits<{'update:open':[value:boolean]}>()
const auth = useAuthStore(), state = ref<HostTerminalState>('connecting'), showFiles = ref(true), mobile = ref(false)
const fileBrowser = ref<{ confirmDiscard: () => Promise<boolean> }>()
const canFiles = computed(() => auth.canAny(['terminal.file.read', 'terminal.file.upload']))
const actor = () => terminalActorContext(auth.user, typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? '')
let openedContext = ''
const stateLabel = computed(() => ({ connecting: '连接中', connected: '已连接', closed: '已结束', error: '连接失败' }[state.value]))
async function close() { if (!fileBrowser.value || await fileBrowser.value.confirmDiscard()) emit('update:open', false) }
function resize() { mobile.value = window.innerWidth <= 700 }
function contextChanged() { if (props.open && (openedContext !== actor() || !auth.canAny(['terminal.open']))) emit('update:open', false) }
watch(() => [props.open, props.path], () => { if (props.open) { openedContext = actor(); state.value = 'connecting' } }, { immediate: true })
watch(actor, contextChanged, { flush: 'sync' })
onMounted(() => { resize(); window.addEventListener('resize', resize); window.addEventListener('storage', contextChanged); window.addEventListener('edo:projects-changed', contextChanged) })
onBeforeUnmount(() => { window.removeEventListener('resize', resize); window.removeEventListener('storage', contextChanged); window.removeEventListener('edo:projects-changed', contextChanged) })
</script>

<template>
  <a-drawer :open="open" :title="title" placement="bottom" height="min(78vh, 850px)" class="terminal-drawer" :mask-closable="false" :keyboard="false" :body-style="{ padding: 0 }" @close="close">
    <template #extra><span class="terminal-connection" :class="state" role="status"><i />{{ stateLabel }}</span><button v-if="canFiles" type="button" class="terminal-files-toggle" :aria-pressed="showFiles" @click="showFiles = !showFiles"><Folder :size="15" />文件</button></template>
    <div v-if="open" class="container-terminal-workspace" :class="{ 'files-shown': showFiles && canFiles }">
      <TerminalFileBrowser v-if="canFiles" v-show="showFiles" ref="fileBrowser" :key="path" :path="path" />
      <div v-show="!mobile || !showFiles || !canFiles" class="container-terminal-pane"><TerminalSessionPane :key="path" :path="path" :visible="open && (!mobile || !showFiles || !canFiles)" @state="state = $event" /></div>
      <button v-if="mobile && showFiles && canFiles" class="mobile-console-return" type="button" @click="showFiles = false"><TerminalSquare :size="16" />返回终端</button>
    </div>
  </a-drawer>
</template>
<style scoped>
.container-terminal-workspace{display:flex;height:calc(min(78vh,850px) - 58px);min-height:220px;min-width:0;position:relative}.container-terminal-pane{flex:1;min-width:0;min-height:0}.terminal-connection{display:inline-flex;align-items:center;gap:6px;margin-right:12px;font-size:12px;color:var(--edo-muted)}.terminal-connection i{width:6px;height:6px;border-radius:50%;background:var(--edo-muted)}.terminal-connection.connected i{background:var(--edo-success,#22a06b)}.terminal-connection.error i{background:var(--edo-error,#d33f57)}.terminal-files-toggle,.mobile-console-return{display:inline-flex;align-items:center;justify-content:center;gap:6px;min-height:30px;padding:4px 9px;border:1px solid var(--edo-border);border-radius:4px;background:var(--edo-surface);color:var(--edo-text);cursor:pointer}.terminal-files-toggle[aria-pressed="true"]{background:var(--edo-primary-soft);color:var(--edo-primary)}.terminal-files-toggle:focus-visible,.mobile-console-return:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.mobile-console-return{position:absolute;bottom:12px;right:12px;box-shadow:0 3px 12px #0002}@media(max-width:700px){.container-terminal-workspace{height:calc(78vh - 68px)}.terminal-connection{margin-right:6px}.terminal-files-toggle{min-height:34px}}
</style>
