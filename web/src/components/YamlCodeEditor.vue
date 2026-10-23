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
import { usePreferencesStore } from '@/stores/preferences'
import { readKubeYaml, type YamlIssue } from '@/utils/kubernetesYaml'

const props = withDefaults(defineProps<{ modelValue: string; readonly?: boolean; issues?: YamlIssue[]; label?: string }>(), { readonly: false })
const emit = defineEmits<{ 'update:modelValue': [value: string]; format: [] }>()
const { t, locale } = useI18n(), preferences = usePreferencesStore(), host = ref<HTMLElement>()
const appearance = new Compartment(), accessibility = new Compartment()
const editing = new Compartment()
const highlighting = new Compartment()
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
  '&': { backgroundColor: 'var(--edo-surface)', color: 'var(--edo-text)', fontSize: '12px' },
  '.cm-content': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', padding: '10px 0', caretColor: 'var(--edo-primary)' },
  '.cm-scroller': { overflow: 'auto', minHeight: '260px', maxHeight: '62vh', lineHeight: '1.75' },
  '.cm-gutters': { backgroundColor: 'var(--edo-bg)', color: 'var(--edo-muted)', borderColor: 'var(--edo-border)' },
  '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'var(--edo-primary-soft, rgba(82,102,255,.07))' },
  '&.cm-focused': { outline: 'none' },
  '.cm-tooltip': { backgroundColor: 'var(--edo-surface)', color: 'var(--edo-text)', borderColor: 'var(--edo-border)' },
}, { dark: preferences.theme === 'dark' })
onMounted(() => {
  editor = new EditorView({ parent: host.value, state: EditorState.create({ doc: props.modelValue, extensions: [
    yaml(), lineNumbers(), foldGutter(), bracketMatching(), drawSelection(), history(),
    ...(props.readonly ? [] : [highlightActiveLine(), highlightActiveLineGutter()]),
    highlighting.of(highlight()), appearance.of(theme()),
    accessibility.of(EditorView.contentAttributes.of({ 'aria-label': props.label || t('yamlEditor.label') })),
    editing.of([EditorState.readOnly.of(props.readonly), EditorView.editable.of(!props.readonly)]),
    keymap.of([{ key: 'Mod-Shift-f', run: () => { if (!props.readonly) emit('format'); return true } }, ...defaultKeymap, ...historyKeymap]),
    EditorView.updateListener.of(update => { if (update.docChanged && update.state.doc.toString() !== props.modelValue) emit('update:modelValue', update.state.doc.toString()) }),
    linter(view => (props.issues ?? readKubeYaml(view.state.doc.toString()).issues).map(issue => ({ from: Math.min(issue.from, view.state.doc.length), to: Math.min(issue.to, view.state.doc.length), severity: 'error' as const, message: t(`yamlEditor.error.${issue.code}`) })), { delay: 200 }),
  ] }) })
})
watch(() => props.modelValue, value => { if (editor && editor.state.doc.toString() !== value) editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: value } }) })
watch(() => props.issues, () => { if (editor) forceLinting(editor) })
watch(() => props.readonly, value => editor?.dispatch({ effects: editing.reconfigure([EditorState.readOnly.of(value), EditorView.editable.of(!value)]) }))
watch(() => preferences.theme, () => editor?.dispatch({ effects: [appearance.reconfigure(theme()), highlighting.reconfigure(highlight())] }))
watch([locale, () => props.label], () => { editor?.dispatch({ effects: accessibility.reconfigure(EditorView.contentAttributes.of({ 'aria-label': props.label || t('yamlEditor.label') })) }); if (editor) forceLinting(editor) })
onBeforeUnmount(() => { editor?.destroy(); editor = undefined })
function focusIssue(issue: YamlIssue) { editor?.dispatch({ selection: { anchor: Math.min(issue.from, editor.state.doc.length) }, scrollIntoView: true }); editor?.focus() }
defineExpose({ focusIssue })
</script>

<template><div ref="host" class="yaml-code-editor" /></template>
<style scoped>.yaml-code-editor{min-width:0;border:1px solid var(--edo-border);border-radius:5px;overflow:hidden}.yaml-code-editor:focus-within{border-color:var(--edo-primary)}</style>
