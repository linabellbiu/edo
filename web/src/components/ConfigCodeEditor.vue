<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Compartment, EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection } from '@codemirror/view'
import { yaml } from '@codemirror/lang-yaml'
import { bracketMatching, foldGutter, syntaxHighlighting, HighlightStyle } from '@codemirror/language'
import { tags } from '@lezer/highlight'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { linter, forceLinting } from '@codemirror/lint'
import { unifiedMergeView, getOriginalDoc, originalDocChangeEffect } from '@codemirror/merge'
import { ChangeSet } from '@codemirror/state'
import { usePreferencesStore } from '@/stores/preferences'
import { json } from '@codemirror/lang-json'
import { checkConfigText, formatConfigText, type ConfigFormat, type ConfigIssue } from '@/utils/configText'

const props = withDefaults(defineProps<{ modelValue: string; readonly?: boolean; format: ConfigFormat; label?: string; original?: string; originalLabel?: string; modifiedLabel?: string }>(), { readonly: false })
const emit = defineEmits<{ 'update:modelValue': [value: string]; format: [] }>()
const { t, locale } = useI18n(), preferences = usePreferencesStore(), host = ref<HTMLElement>()
const appearance = new Compartment(), accessibility = new Compartment()
const editing = new Compartment(), language = new Compartment()
const highlighting = new Compartment()
const comparison = new Compartment()
function comparisonExtension() { return props.original === undefined ? [] : unifiedMergeView({ original: props.original, mergeControls: false, allowInlineDiffs: false, diffConfig: { scanLimit: 500, timeout: 50 } }) }
function highlight() {
  const dark = preferences.theme === 'dark'
  return syntaxHighlighting(HighlightStyle.define([
    { tag: [tags.propertyName, tags.attributeName], color: dark ? '#9bc3ff' : '#315c9f' },
    { tag: [tags.string, tags.special(tags.string)], color: dark ? '#a7d9a0' : '#356b3e' },
    { tag: [tags.number, tags.bool, tags.null, tags.atom], color: dark ? '#f0bf83' : '#946020' },
    { tag: tags.comment, color: dark ? '#99a3b5' : '#687589', fontStyle: 'italic' },
    { tag: [tags.meta, tags.keyword, tags.typeName], color: dark ? '#c6b4f5' : '#7453a6' },
    { tag: tags.invalid, color: dark ? '#ff9ba7' : '#be3148' },
  ]))
}
let editor: EditorView | undefined
const theme = () => EditorView.theme({
  '&': { backgroundColor: 'var(--edo-surface)', color: 'var(--edo-text)', fontSize: '13px' },
  '.cm-content': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', padding: '10px 0', caretColor: 'var(--edo-primary)' },
  '.cm-scroller': { overflow: 'auto', minHeight: '260px', maxHeight: '62vh', lineHeight: '1.75' },
  '.cm-gutters': { backgroundColor: 'var(--edo-bg)', color: 'var(--edo-muted)', borderColor: 'var(--edo-border)' },
  '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'var(--edo-primary-soft, rgba(82,102,255,.07))' },
  '&.cm-focused': { outline: 'none' },
  '.cm-tooltip': { backgroundColor: 'var(--edo-surface)', color: 'var(--edo-text)', borderColor: 'var(--edo-border)' },
  '.cm-insertedLine': { backgroundColor: preferences.theme === 'dark' ? '#173b2b' : '#e8f5eb' },
  '.cm-deletedChunk': { backgroundColor: preferences.theme === 'dark' ? '#46252b' : '#fff0f0' },
}, { dark: preferences.theme === 'dark' })
onMounted(() => {
  editor = new EditorView({ parent: host.value, state: EditorState.create({ doc: props.modelValue, extensions: [
    language.of(props.format==='yaml'?yaml():props.format==='json'?json():[]), lineNumbers(), foldGutter(), bracketMatching(), drawSelection(), history(),
    ...(props.readonly ? [] : [highlightActiveLine(), highlightActiveLineGutter()]),
    highlighting.of(highlight()), appearance.of(theme()), comparison.of(comparisonExtension()),
    accessibility.of(EditorView.contentAttributes.of({ 'aria-label': props.label || '配置内容' })),
    editing.of([EditorState.readOnly.of(props.readonly), EditorView.editable.of(!props.readonly)]),
    keymap.of([{ key: 'Mod-Shift-f', run: () => { if (!props.readonly) formatDocument(); return true } }, ...defaultKeymap, ...historyKeymap]),
    EditorView.updateListener.of(update => { if (update.docChanged && update.state.doc.toString() !== props.modelValue) emit('update:modelValue', update.state.doc.toString()) }),
    linter(view => checkConfigText(view.state.doc.toString(),props.format).map(issue => ({from:issue.from,to:issue.to,severity:issue.severity,message:issue.message})), {delay:250}),
  ] }) })
})
watch(() => props.modelValue, value => { if (editor && editor.state.doc.toString() !== value) editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: value } }) })
watch(() => props.original, (value, previous) => {
  if (!editor) return
  if (value === undefined || previous === undefined) { editor.dispatch({ effects: comparison.reconfigure(comparisonExtension()) }); return }
  const original = getOriginalDoc(editor.state)
  editor.dispatch({ effects: originalDocChangeEffect(editor.state, ChangeSet.of({ from: 0, to: original.length, insert: value }, original.length)) })
})
watch(() => props.format, value => { editor?.dispatch({effects:language.reconfigure(value==='yaml'?yaml():value==='json'?json():[])}); if(editor)forceLinting(editor) })
watch(() => props.readonly, value => editor?.dispatch({ effects: editing.reconfigure([EditorState.readOnly.of(value), EditorView.editable.of(!value)]) }))
watch(() => preferences.theme, () => editor?.dispatch({ effects: [appearance.reconfigure(theme()), highlighting.reconfigure(highlight())] }))
watch([locale, () => props.label], () => { editor?.dispatch({ effects: accessibility.reconfigure(EditorView.contentAttributes.of({ 'aria-label': props.label || '配置内容' })) }); if (editor) forceLinting(editor) })
onBeforeUnmount(() => { editor?.destroy(); editor = undefined })
function formatDocument(){if(props.readonly||!editor)return;const value=formatConfigText(editor.state.doc.toString(),props.format);if(value!==undefined)editor.dispatch({changes:{from:0,to:editor.state.doc.length,insert:value}})}
function focusIssue(issue: ConfigIssue) { editor?.dispatch({ selection: { anchor: Math.min(issue.from, editor.state.doc.length) }, scrollIntoView: true }); editor?.focus() }
defineExpose({ focusIssue })
</script>

<template><div class="config-editor-shell"><div v-if="original!==undefined" class="config-diff-legend"><span class="removed">− {{ originalLabel || '修改前' }}</span><span class="added">+ {{ modifiedLabel || '修改后' }}</span><span v-if="original===modelValue">内容相同</span></div><a-button v-if="format==='yaml'&&!readonly" size="small" :disabled="checkConfigText(modelValue,format).some(issue=>issue.severity==='error')" @click="formatDocument">格式化 YAML</a-button><div ref="host" class="config-code-editor" /></div></template>
<style scoped>.config-code-editor{min-width:0;border:1px solid var(--edo-border);border-radius:5px;overflow:hidden}.config-code-editor:focus-within{border-color:var(--edo-primary)}</style>

<style scoped>.config-editor-shell{display:grid;gap:8px;min-width:0}.config-editor-shell>.ant-btn{justify-self:start}</style>
<style scoped>.config-diff-legend{display:flex;flex-wrap:wrap;gap:8px 16px;font-size:12px;color:var(--edo-muted)}.config-diff-legend .removed{color:var(--edo-danger,#c33)}.config-diff-legend .added{color:var(--edo-success,#258344)}</style>
