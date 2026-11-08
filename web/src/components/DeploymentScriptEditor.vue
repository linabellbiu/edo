<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Compartment, EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, drawSelection } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { linter, forceLinting } from '@codemirror/lint'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ modelValue: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const auth = useAuthStore(), host = ref<HTMLElement>(), upload = ref<HTMLInputElement>()
type Issue = { line: number; column: number; message: string }
const issues = ref<Issue[]>([]), status = ref(''), error = ref(''), checking = ref(false)
const editing = new Compartment()
let editor: EditorView | undefined, controller: AbortController | undefined, timer: ReturnType<typeof setTimeout> | undefined, revision = 0, disposed = false
function identity() { return JSON.stringify([auth.user?.id, auth.user?.project_id, auth.user?.permissions, auth.user?.is_superuser, auth.authorizationState, localStorage.getItem('edo.project')]) }
function canEdit() { return !props.disabled && auth.canAny(['workflow.update']) && auth.user?.project_id === localStorage.getItem('edo.project') }
async function check() {
  controller?.abort(); controller = new AbortController(); const signal = controller.signal, request = ++revision, scope = identity(), script = props.modelValue
  issues.value = []; status.value = ''; error.value = ''; checking.value = false
  if (!canEdit() || !script.trim()) { if (editor) forceLinting(editor); return }
  checking.value = true
  try {
    const { data } = await client.post<{ issues: Issue[] }>('/workflows/deployment-script/check', { script }, { signal, headers: { 'X-EDO-Project-ID': auth.user!.project_id } })
    if (disposed || signal.aborted || request !== revision || scope !== identity() || script !== props.modelValue) return
    issues.value = data.issues; status.value = data.issues.length ? 'Shell 语法检查未通过' : 'Shell 语法检查通过（POSIX sh）'
    if (editor) forceLinting(editor)
  } catch (cause) { if (!signal.aborted && scope === identity() && request === revision) error.value = apiErrorMessage(cause) }
  finally { if (request === revision) checking.value = false }
}
function schedule() { clearTimeout(timer); controller?.abort(); revision++; issues.value = []; status.value = ''; error.value = ''; checking.value = false; timer = setTimeout(() => { void check() }, 500) }
async function readUpload(event: Event) {
  const input = event.target as HTMLInputElement, file = input.files?.[0], scope = identity(), original = props.modelValue
  input.value = ''; if (!file || !canEdit()) return
  if (file.size > 256 * 1024) { error.value = '部署脚本不能超过 256 KiB'; return }
  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(await file.arrayBuffer())
    if (disposed || scope !== identity() || !canEdit() || original !== props.modelValue) return
    if (text.includes('\0')) { error.value = '请选择 UTF-8 Shell 文本文件'; return }
    emit('update:modelValue', text)
  } catch { if (scope === identity()) error.value = '无法读取文件，请选择 UTF-8 Shell 文本文件' }
}
onMounted(() => {
  editor = new EditorView({ parent: host.value, state: EditorState.create({ doc: props.modelValue, extensions: [
    lineNumbers(), drawSelection(), history(), keymap.of([...defaultKeymap, ...historyKeymap]),
    EditorView.contentAttributes.of({ 'aria-label': '部署脚本', spellcheck: 'false' }),
    editing.of([EditorState.readOnly.of(!canEdit()), EditorView.editable.of(canEdit())]),
    EditorView.theme({ '&': { background: 'var(--edo-surface)', color: 'var(--edo-text)', fontSize: '13px' }, '.cm-content': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', minHeight: '200px' }, '.cm-scroller': { overflow: 'auto', maxHeight: '50vh', lineHeight: '1.7' }, '.cm-gutters': { background: 'var(--edo-bg)', color: 'var(--edo-muted)', borderColor: 'var(--edo-border)' } }),
    EditorView.updateListener.of(update => { if (update.docChanged && canEdit() && update.state.doc.toString() !== props.modelValue) emit('update:modelValue', update.state.doc.toString()) }),
    linter(view => issues.value.map(issue => { const line = view.state.doc.line(Math.min(Math.max(1, issue.line), view.state.doc.lines)), from = Math.min(line.to, line.from + Math.max(0, issue.column - 1)); return { from, to: Math.min(line.to, from + 1), severity: 'error' as const, message: issue.message } })),
  ] }) }); schedule()
})
watch(() => props.modelValue, value => { if (editor && editor.state.doc.toString() !== value) editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: value } }); schedule() })
watch([identity, () => props.disabled], () => { revision++; controller?.abort(); issues.value = []; status.value = ''; error.value = ''; checking.value = false; editor?.dispatch({ effects: editing.reconfigure([EditorState.readOnly.of(!canEdit()), EditorView.editable.of(canEdit())]) }); if (canEdit()) schedule() })
onBeforeUnmount(() => { disposed = true; revision++; clearTimeout(timer); controller?.abort(); editor?.destroy() })
function locate(issue: Issue) { if (!editor) return; const line = editor.state.doc.line(Math.min(issue.line, editor.state.doc.lines)); editor.dispatch({ selection: { anchor: Math.min(line.to, line.from + issue.column - 1) }, scrollIntoView: true }); editor.focus() }
</script>
<template>
 <section class="deployment-script-editor" aria-label="部署脚本编辑器">
  <div class="script-actions"><a-button :disabled="disabled" @click="upload?.click()">上传部署脚本</a-button><a-button :disabled="disabled || !modelValue.trim()" :loading="checking" @click="check">检查 Shell 语法</a-button><input ref="upload" type="file" accept=".sh,.bash,.txt,text/plain,application/x-sh" hidden @change="readUpload" /></div>
  <div ref="host" class="script-code" />
  <p v-if="status" :role="issues.length ? 'alert' : 'status'">{{ status }}</p><a-alert v-if="error" type="error" :message="error" show-icon />
  <button v-for="(issue, index) in issues" :key="index" class="script-issue" type="button" @click="locate(issue)">第 {{ issue.line }} 行，第 {{ issue.column }} 列：{{ issue.message }}</button>
 </section>
</template>
<style scoped>.deployment-script-editor{display:grid;gap:8px;min-width:0}.script-actions{display:flex;flex-wrap:wrap;gap:8px}.script-code{min-width:0;border:1px solid var(--edo-border);border-radius:5px;overflow:hidden}.script-code:focus-within{border-color:var(--edo-primary)}.deployment-script-editor p{margin:0;color:var(--edo-muted);font-size:12px}.script-issue{text-align:left;color:var(--edo-danger);border:0;background:transparent;cursor:pointer}.script-issue:focus-visible{outline:2px solid var(--edo-primary)}</style>
