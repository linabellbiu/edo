<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { ArrowUp, ChevronRight, Download, File, Folder, Link, RefreshCw, Save, Upload } from 'lucide-vue-next'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { terminalActorContext } from '@/stores/hostTerminals'
import { terminalChild, terminalFileSize, terminalFileTarget, terminalParent, terminalTextLimit, terminalTransferLimit } from '@/utils/terminalTarget'

interface Entry { name: string; path: string; kind: 'directory' | 'file' | 'symlink' | 'other'; size: number; modified_at?: string; mode?: number; etag?: string }
interface Content extends Entry { content: string; etag: string }
const props = defineProps<{ path: string }>()
const auth = useAuthStore(), directory = ref('/'), address = ref('/'), entries = ref<Entry[]>([])
const loading = ref(false), busy = ref(false), error = ref(''), truncated = ref(false), uploadInput = ref<HTMLInputElement>()
const editor = ref<Content>(), draft = ref(''), fileError = ref(''), reading = ref(false), saving = ref(false)
const canRead = computed(() => Boolean(auth.user) && auth.canAny(['terminal.file.read']))
const canUpload = computed(() => Boolean(auth.user) && auth.canAny(['terminal.file.upload']))
const canEdit = computed(() => canRead.value && auth.canAny(['terminal.file.edit']))
const dirty = computed(() => Boolean(editor.value && editor.value.content !== draft.value))
const draftSize = computed(() => new TextEncoder().encode(draft.value).byteLength)
const target = computed(() => terminalFileTarget(props.path))
const items = computed(() => [...entries.value].sort((a, b) => Number(b.kind === 'directory') - Number(a.kind === 'directory') || a.name.localeCompare(b.name)))
let disposed = false, revision = 0, fileRevision = 0
let listController: AbortController | undefined, fileController: AbortController | undefined, transferController: AbortController | undefined
let confirmation: ReturnType<typeof Modal.confirm> | undefined
const actor = () => terminalActorContext(auth.user, typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? '')
const context = () => JSON.stringify([actor(), props.path])
let frozenContext = context()
const valid = (identity: string) => !disposed && identity === frozenContext && identity === context() && Boolean(target.value)
function options(path: string, signal?: AbortSignal) {
  return { params: { ...target.value?.parameters, path }, headers: { 'X-EDO-Project-ID': target.value?.parameters.project_id ?? (typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? '') }, signal }
}
function reset() {
  frozenContext = context(); revision++; fileRevision++; listController?.abort(); fileController?.abort(); transferController?.abort(); confirmation?.destroy(); confirmation = undefined
  entries.value = []; error.value = ''; fileError.value = ''; editor.value = undefined; draft.value = ''; directory.value = '/'; address.value = '/'; loading.value = false; busy.value = false; reading.value = false; saving.value = false; truncated.value = false
  if (canRead.value && target.value) void load('/')
}
async function load(path = address.value) {
  if (!canRead.value || !target.value || disposed) return
  const identity = context(), request = ++revision
  listController?.abort(); const controller = new AbortController(); listController = controller
  loading.value = true; error.value = ''
  try {
    const { data } = await client.get<{ directory: { path: string; entries: Entry[]; truncated: boolean } }>(target.value.base, options(path, controller.signal))
    if (!valid(identity) || request !== revision || !canRead.value) return
    directory.value = data.directory.path; address.value = data.directory.path; entries.value = data.directory.entries; truncated.value = data.directory.truncated
  } catch (err) { if (valid(identity) && request === revision && !controller.signal.aborted) { entries.value = []; error.value = apiErrorMessage(err) } }
  finally { if (valid(identity) && request === revision) loading.value = false }
}
async function confirmDiscard() {
  if (!dirty.value) return true
  const identity = context()
  return await new Promise<boolean>(resolve => {
    confirmation = Modal.confirm({ title: '放弃尚未保存的修改？', content: editor.value?.path, zIndex: 1120, okText: '放弃修改', cancelText: '继续编辑', onOk: () => resolve(valid(identity)), onCancel: () => resolve(false), afterClose: () => { confirmation = undefined; resolve(false) } })
  })
}
async function closeEditor() { if (await confirmDiscard()) { fileRevision++; fileController?.abort(); editor.value = undefined; draft.value = ''; fileError.value = '' } }
async function preview(entry: Entry) {
  if (entry.kind !== 'file' || !canRead.value || !target.value || reading.value || !await confirmDiscard()) return
  if (entry.size > terminalTextLimit) { message.warning('文本预览与编辑仅支持不超过 1 MiB 的 UTF-8 文件'); return }
  const identity = context(), request = ++fileRevision
  fileController?.abort(); const controller = new AbortController(); fileController = controller
  reading.value = true; fileError.value = ''
  try {
    const { data } = await client.get<{ file: Content }>(`${target.value.base}/content`, options(entry.path, controller.signal))
    if (!valid(identity) || request !== fileRevision || !canRead.value) return
    editor.value = data.file; draft.value = data.file.content
  } catch (err) { if (valid(identity) && request === fileRevision && !controller.signal.aborted) error.value = apiErrorMessage(err) }
  finally { if (valid(identity) && request === fileRevision) reading.value = false }
}
async function save() {
  if (!editor.value || !target.value || !canEdit.value || !dirty.value || saving.value) return
  if (new TextEncoder().encode(draft.value).byteLength > terminalTextLimit) { fileError.value = '文件内容超过 1 MiB，无法保存'; return }
  const identity = context(), file = editor.value, content = draft.value
  saving.value = true; fileError.value = ''
  fileController?.abort(); const controller = new AbortController(); fileController = controller
  try {
    const { data } = await client.put<{ file: Entry & { etag: string } }>(`${target.value.base}/content`, { path: file.path, content, etag: file.etag }, { ...options(file.path, controller.signal), timeout: 60_000 })
    if (!valid(identity) || editor.value !== file || !canEdit.value) return
    editor.value = { ...data.file, content }; draft.value = content; message.success('文件已保存'); void load(directory.value)
  } catch (err) { if (valid(identity) && !controller.signal.aborted) fileError.value = apiErrorMessage(err) }
  finally { if (valid(identity)) saving.value = false }
}
async function download(entry: Entry) {
  if (!target.value || !canRead.value || entry.kind !== 'file' || busy.value) return
  if (entry.size > terminalTransferLimit) { message.warning('单个文件传输不能超过 64 MiB'); return }
  const identity = context(); transferController?.abort(); const controller = new AbortController(); transferController = controller
  busy.value = true; error.value = ''
  try {
    const { data } = await client.get<Blob>(`${target.value.base}/download`, { ...options(entry.path, controller.signal), responseType: 'blob', timeout: 120_000 })
    if (!valid(identity) || !canRead.value) return
    if (data.size > terminalTransferLimit) throw new Error('单个文件传输不能超过 64 MiB')
    const url = URL.createObjectURL(data), anchor = document.createElement('a')
    anchor.href = url; anchor.download = entry.name; anchor.click(); window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (err) { if (valid(identity) && !controller.signal.aborted) error.value = apiErrorMessage(err) }
  finally { if (valid(identity)) busy.value = false }
}
async function upload(event: Event) {
  const input = event.target as HTMLInputElement, file = input.files?.[0]; input.value = ''
  if (!file || !target.value || !canUpload.value || busy.value) return
  if (file.size > terminalTransferLimit) { message.warning('单个文件传输不能超过 64 MiB'); return }
  const path = terminalChild(directory.value, file.name)
  if (!path) { message.error('文件名无效'); return }
  const identity = context(); transferController?.abort(); const controller = new AbortController(); transferController = controller
  busy.value = true; error.value = ''
  try {
    let etag = ''
    if (canRead.value) {
      try { const response = await client.get<{ file: Entry & { etag: string } }>(`${target.value.base}/stat`, options(path, controller.signal)); etag = response.data.file.etag }
      catch (err) { if ((err as { response?: { status?: number } }).response?.status !== 404) throw err }
    }
    if (!valid(identity) || !canUpload.value) return
    if (etag) {
      if (!canEdit.value) { error.value = '同名文件已存在，覆盖还需要文件读取和编辑权限'; return }
      const accepted = await new Promise<boolean>(resolve => {
        confirmation = Modal.confirm({ title: '覆盖同名文件？', content: path, zIndex: 1120, okText: '确认覆盖', cancelText: '取消', onOk: () => resolve(valid(identity)), onCancel: () => resolve(false), afterClose: () => { confirmation = undefined; resolve(false) } })
      })
      if (!accepted || !valid(identity) || !canEdit.value || !canUpload.value) return
    }
    await client.post(`${target.value.base}/upload`, file, { ...options(path, controller.signal), headers: { ...options(path).headers, 'Content-Type': 'application/octet-stream', ...(etag ? { 'If-Match': etag } : {}) }, timeout: 120_000 })
    if (!valid(identity)) return
    message.success('文件已上传'); if (canRead.value) void load(directory.value)
  } catch (err) { if (valid(identity) && !controller.signal.aborted) error.value = apiErrorMessage(err) }
  finally { if (valid(identity)) busy.value = false }
}
function uploadHere() { if (!canRead.value && address.value.startsWith('/')) directory.value = address.value; uploadInput.value?.click() }
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value) { event.preventDefault(); event.returnValue = '' } }
function externalContext() { if (frozenContext !== context()) reset() }
watch(() => [props.path, actor()], reset, { immediate: true, flush: 'sync' })
if (typeof window !== 'undefined') { window.addEventListener('beforeunload', beforeUnload); window.addEventListener('storage', externalContext); window.addEventListener('edo:projects-changed', externalContext) }
onBeforeUnmount(() => { disposed = true; revision++; fileRevision++; listController?.abort(); fileController?.abort(); transferController?.abort(); confirmation?.destroy(); if (typeof window !== 'undefined') { window.removeEventListener('beforeunload', beforeUnload); window.removeEventListener('storage', externalContext); window.removeEventListener('edo:projects-changed', externalContext) } })
defineExpose({ confirmDiscard })
</script>

<template>
  <aside class="terminal-files" aria-label="终端文件">
    <header><Folder :size="16" aria-hidden="true" /><strong>文件</strong><span /><button type="button" :disabled="loading || !canRead || !target" aria-label="刷新目录" title="刷新目录" @click="load(directory)"><RefreshCw :size="15" /></button><button v-if="canUpload" type="button" :disabled="busy || !target" aria-label="上传文件" title="上传文件" @click="uploadHere"><Upload :size="15" /></button><input ref="uploadInput" type="file" hidden @change="upload" /></header>
    <form class="file-path" @submit.prevent="load(address)"><button type="button" :disabled="directory === '/' || loading || !canRead" aria-label="上级目录" @click="load(terminalParent(directory))"><ArrowUp :size="15" /></button><input v-model="address" aria-label="文件目录路径" :disabled="loading || !target" /><button type="submit" :disabled="!canRead || loading" aria-label="打开目录"><ChevronRight :size="15" /></button></form>
    <p v-if="!target" class="file-notice" role="alert">文件目标无效</p><p v-else-if="!canRead" class="file-notice">没有读取文件的权限</p><p v-else-if="loading" class="file-notice" role="status">正在读取目录…</p>
    <p v-if="error" class="file-error" role="alert">{{ error }}</p><p v-if="busy" class="file-notice" role="status">正在传输文件…</p><p v-if="reading" class="file-notice" role="status">正在读取文件…</p>
    <ul v-if="canRead" class="file-list" aria-label="目录内容"><li v-for="entry in items" :key="entry.path"><button class="file-name" type="button" :disabled="loading || reading || !['directory','file'].includes(entry.kind)" :title="entry.path" @click="entry.kind === 'directory' ? load(entry.path) : preview(entry)"><Folder v-if="entry.kind === 'directory'" :size="15" aria-hidden="true" /><Link v-else-if="entry.kind === 'symlink'" :size="15" aria-hidden="true" /><File v-else :size="15" aria-hidden="true" /><span>{{ entry.name }}</span></button><span v-if="entry.kind === 'file'" class="file-size">{{ terminalFileSize(entry.size) }}</span><button v-if="entry.kind === 'file'" type="button" class="file-download" :disabled="busy || entry.size > terminalTransferLimit" :aria-label="`下载 ${entry.name}`" :title="`下载 ${entry.name}`" @click="download(entry)"><Download :size="14" /></button></li></ul>
    <p v-if="canRead && !loading && !error && !entries.length" class="file-notice">目录为空</p><p v-if="truncated" class="file-notice">仅显示前 1000 项</p>
    <a-modal :open="Boolean(editor)" :title="editor?.path" width="min(1000px, 94vw)" :z-index="1080" :mask-closable="false" :keyboard="false" :closable="!saving" @cancel="closeEditor">
      <div class="file-editor-meta"><span>{{ canEdit ? 'UTF-8' : '只读 · UTF-8' }}</span><span v-if="dirty">尚未保存</span><span>{{ terminalFileSize(draftSize) }}</span></div><p v-if="fileError" class="file-error" role="alert">{{ fileError }}</p><textarea v-model="draft" class="file-editor" spellcheck="false" :readonly="!canEdit || saving" :aria-label="editor ? `文件内容 ${editor.name}` : '文件内容'" />
      <template #footer><a-button :disabled="saving" @click="closeEditor">关闭</a-button><a-button v-if="canEdit" type="primary" :loading="saving" :disabled="!dirty" @click="save"><Save :size="14" />保存</a-button></template>
    </a-modal>
  </aside>
</template>

<style scoped>
.terminal-files{display:flex;flex-direction:column;width:280px;min-width:0;flex:0 0 280px;background:var(--edo-surface);color:var(--edo-text);border-right:1px solid var(--edo-border);overflow:hidden;font-size:12px}.terminal-files>header{display:flex;align-items:center;gap:7px;min-height:40px;padding:5px 10px;border-bottom:1px solid var(--edo-border);background:var(--edo-surface-soft)}.terminal-files>header>span{flex:1}.terminal-files button{display:inline-flex;align-items:center;justify-content:center;gap:7px;min-width:28px;min-height:28px;padding:3px;border:0;border-radius:4px;background:transparent;color:var(--edo-text);cursor:pointer}.terminal-files button:hover:not(:disabled){background:var(--edo-primary-soft);color:var(--edo-primary)}.terminal-files button:disabled{opacity:.48;cursor:not-allowed}.terminal-files button:focus-visible,.file-path input:focus-visible,.file-editor:focus-visible{outline:2px solid var(--edo-primary);outline-offset:-2px}.file-path{display:flex;gap:3px;margin:8px;border:1px solid var(--edo-border);border-radius:4px;min-height:32px}.file-path input{min-width:0;width:0;flex:1;border:0;background:transparent;color:var(--edo-text);font:inherit}.file-list{min-height:0;overflow:auto;list-style:none;padding:0 5px 8px;margin:0;flex:1}.file-list li{display:flex;align-items:center;gap:3px;min-height:32px;border-radius:4px}.file-list li:hover{background:var(--edo-surface-soft)}.file-list .file-name{flex:1;min-width:0;justify-content:flex-start;text-align:left;padding:5px 6px}.file-name svg{flex-shrink:0;color:var(--edo-primary)}.file-name span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.file-size{white-space:nowrap;color:var(--edo-muted);font-size:11px;font-variant-numeric:tabular-nums}.file-notice,.file-error{margin:6px 10px;line-height:1.6;overflow-wrap:anywhere}.file-notice{color:var(--edo-muted)}.file-error{color:var(--edo-error,#c5304d)}.file-editor-meta{display:flex;gap:12px;align-items:center;margin-bottom:8px;font-size:12px;color:var(--edo-muted)}.file-editor-meta>span:last-child{margin-left:auto}.file-editor{display:block;width:100%;height:min(60vh,600px);resize:vertical;padding:12px;border:1px solid var(--edo-border);border-radius:4px;background:var(--edo-surface-soft);color:var(--edo-text);font:13px/1.6 SFMono-Regular,Consolas,monospace;tab-size:2;white-space:pre}.terminal-files :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}@media(max-width:700px){.terminal-files{width:100%;flex:1;border-right:0}.terminal-files button{min-width:34px;min-height:34px}.file-list li{min-height:38px}}
.terminal-files{--terminal-link:#3859cc}.terminal-files button:hover:not(:disabled){color:var(--terminal-link)}:global([data-theme="dark"] .terminal-files){--terminal-link:#a5b7ff}
</style>
