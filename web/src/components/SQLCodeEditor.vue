<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Compartment, EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection } from '@codemirror/view'
import { bracketMatching, foldGutter, syntaxHighlighting, HighlightStyle } from '@codemirror/language'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { sql, MySQL, PostgreSQL, SQLite, StandardSQL } from '@codemirror/lang-sql'
import { linter, forceLinting } from '@codemirror/lint'
import { tags } from '@lezer/highlight'
import { usePreferencesStore } from '@/stores/preferences'
import type { SQLFinding } from '@/api/sqlChanges'

const props = withDefaults(defineProps<{ modelValue: string; readonly?: boolean; dialect?: string; issues?: SQLFinding[]; label?: string }>(), { readonly: false, dialect: 'postgres' })
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const preferences = usePreferencesStore(), { t } = useI18n(), host = ref<HTMLElement>()
const appearance = new Compartment(), editing = new Compartment(), language = new Compartment(), highlighting = new Compartment(), accessibility = new Compartment()
let editor: EditorView | undefined
const dialect = () => ({ mysql: MySQL, postgres: PostgreSQL, sqlite: SQLite }[props.dialect] ?? StandardSQL)
const highlight = () => syntaxHighlighting(HighlightStyle.define([
  { tag: tags.keyword, color: preferences.theme === 'dark' ? '#c6b4f5' : '#7453a6' },
  { tag: [tags.string, tags.special(tags.string)], color: preferences.theme === 'dark' ? '#a7d9a0' : '#356b3e' },
  { tag: [tags.number, tags.bool, tags.null], color: preferences.theme === 'dark' ? '#f0bf83' : '#946020' },
  { tag: tags.comment, color: preferences.theme === 'dark' ? '#99a3b5' : '#687589', fontStyle: 'italic' },
  { tag: [tags.typeName, tags.function(tags.variableName)], color: preferences.theme === 'dark' ? '#9bc3ff' : '#315c9f' },
]))
const theme = () => EditorView.theme({
  '&': { backgroundColor: 'var(--edo-surface)', color: 'var(--edo-text)', fontSize: '13px' },
  '.cm-content': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', padding: '10px 0', caretColor: 'var(--edo-primary)' },
  '.cm-scroller': { overflow: 'auto', minHeight: '240px', maxHeight: '48vh', lineHeight: '1.75' },
  '.cm-gutters': { backgroundColor: 'var(--edo-bg)', color: 'var(--edo-muted)', borderColor: 'var(--edo-border)' },
  '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'var(--edo-primary-soft)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-tooltip': { backgroundColor: 'var(--edo-surface)', color: 'var(--edo-text)', borderColor: 'var(--edo-border)' },
}, { dark: preferences.theme === 'dark' })
function offset(line?: number, column?: number) {
  if (!editor) return 0
  const number = Math.min(Math.max(1, Math.trunc(line || 1)), editor.state.doc.lines)
  const target = editor.state.doc.line(number)
  return Math.min(target.to, target.from + Math.max(0, Math.trunc(column || 1) - 1))
}
onMounted(() => {
  editor = new EditorView({ parent: host.value, state: EditorState.create({ doc: props.modelValue, extensions: [
    language.of(sql({ dialect: dialect() })), lineNumbers(), foldGutter(), bracketMatching(), drawSelection(), history(),
    highlightActiveLine(), highlightActiveLineGutter(), highlighting.of(highlight()), appearance.of(theme()),
    accessibility.of(EditorView.contentAttributes.of({ 'aria-label': props.label || t('sqlChanges.sqlLabel') })),
    editing.of([EditorState.readOnly.of(props.readonly), EditorView.editable.of(!props.readonly)]), keymap.of([...defaultKeymap, ...historyKeymap]),
    EditorView.updateListener.of(update => { if (update.docChanged && update.state.doc.toString() !== props.modelValue) emit('update:modelValue', update.state.doc.toString()) }),
    linter(() => (props.issues ?? []).filter(issue => Number.isInteger(issue.line) && Number(issue.line) > 0 && Number.isInteger(issue.column) && Number(issue.column) > 0).map(issue => ({ from: offset(issue.line, issue.column), to: offset(issue.line, issue.column), severity: issue.severity === 'warning' ? 'warning' as const : 'error' as const, message: issue.message || issue.description || issue.rule || '' }))),
  ] }) })
})
watch(() => props.modelValue, value => { if (editor && editor.state.doc.toString() !== value) editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: value } }) })
watch(() => props.dialect, () => editor?.dispatch({ effects: language.reconfigure(sql({ dialect: dialect() })) }))
watch(() => props.readonly, value => editor?.dispatch({ effects: editing.reconfigure([EditorState.readOnly.of(value), EditorView.editable.of(!value)]) }))
watch(() => props.issues, () => { if (editor) forceLinting(editor) })
watch(() => preferences.theme, () => editor?.dispatch({ effects: [appearance.reconfigure(theme()), highlighting.reconfigure(highlight())] }))
watch(() => props.label || t('sqlChanges.sqlLabel'), value => editor?.dispatch({ effects: accessibility.reconfigure(EditorView.contentAttributes.of({ 'aria-label': value })) }))
onBeforeUnmount(() => { editor?.destroy(); editor = undefined })
function focusIssue(issue: SQLFinding) { if (!editor || !Number.isInteger(issue.line) || Number(issue.line) < 1 || !Number.isInteger(issue.column) || Number(issue.column) < 1) return; editor.dispatch({ selection: { anchor: offset(issue.line, issue.column) }, scrollIntoView: true }); editor.focus() }
defineExpose({ focusIssue })
</script>
<template><div ref="host" class="sql-code-editor" /></template>
<style scoped>.sql-code-editor{min-width:0;border:1px solid var(--edo-border);border-radius:6px;overflow:hidden}.sql-code-editor:focus-within{border-color:var(--edo-primary)}</style>
