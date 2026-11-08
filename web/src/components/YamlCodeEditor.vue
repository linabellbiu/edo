<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch, useId } from 'vue'
import { Maximize, ChevronDown, ChevronUp } from 'lucide-vue-next'
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

const props = withDefaults(defineProps<{ modelValue: string; readonly?: boolean; issues?: YamlIssue[]; label?: string; fullscreen?: boolean; collapsible?:boolean; defaultCollapsed?:boolean }>(), { readonly: false, fullscreen: false, collapsible:false, defaultCollapsed:false })
const emit = defineEmits<{ 'update:modelValue': [value: string]; format: [] }>()
const { t, locale } = useI18n(), preferences = usePreferencesStore(), host = ref<HTMLElement>()
const appearance = new Compartment(), accessibility = new Compartment()
const editing = new Compartment()
const highlighting = new Compartment()
const fullscreenOpen = ref(false), fullscreenHost = ref<HTMLElement>()
const collapsed = ref(props.collapsible && props.defaultCollapsed), editorID = useId()
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
watch(fullscreenOpen, async open => { await nextTick(); editor?.requestMeasure(); if (open) editor?.focus() })
watch(collapsed, async value => { if(!value){await nextTick();editor?.requestMeasure()} })
watch(() => props.fullscreen, enabled => { if (!enabled) fullscreenOpen.value = false })
onBeforeUnmount(() => { editor?.destroy(); editor = undefined })
function focusIssue(issue: YamlIssue) { editor?.dispatch({ selection: { anchor: Math.min(issue.from, editor.state.doc.length) }, scrollIntoView: true }); editor?.focus() }
defineExpose({ focusIssue })
</script>

<template>
  <div class="yaml-editor-shell">
    <div v-if="fullscreen||collapsible||$slots.toolbar" class="yaml-editor-toolbar"><div v-if="$slots.toolbar" class="yaml-editor-heading"><slot name="toolbar"/></div><div class="yaml-editor-actions"><a-button v-if="collapsible" size="small" :aria-label="`${collapsed?'展开':'收起'} ${label || 'YAML'}`" :aria-expanded="!collapsed" :aria-controls="editorID" @click="collapsed=!collapsed"><ChevronDown v-if="collapsed" :size="14"/><ChevronUp v-else :size="14"/>{{collapsed?'展开':'收起'}}</a-button><a-button v-if="fullscreen" size="small" :aria-label="`全屏查看或编辑 ${label || 'YAML'}`" @click="fullscreenOpen = true"><Maximize :size="14" />全屏</a-button></div></div>
    <Teleport :to="fullscreenHost || 'body'" :disabled="!fullscreenOpen || !fullscreenHost"><div :id="editorID" v-show="!collapsed||fullscreenOpen" ref="host" class="yaml-code-editor" /></Teleport>
    <a-modal v-if="fullscreen" v-model:open="fullscreenOpen" :title="label || 'YAML'" :footer="null" :force-render="true" width="100vw" :style="{ top: '0', maxWidth: '100vw', paddingBottom: 0 }" wrap-class-name="yaml-editor-fullscreen-modal"><div ref="fullscreenHost" class="yaml-editor-fullscreen-host" /></a-modal>
  </div>
</template>
<style scoped>.yaml-code-editor{min-width:0;border:1px solid var(--edo-border);border-radius:5px;overflow:hidden}.yaml-code-editor:focus-within{border-color:var(--edo-primary)}</style>
<style scoped>
.yaml-editor-shell{min-width:0}.yaml-editor-toolbar{display:flex;align-items:center;justify-content:flex-end;flex-wrap:wrap;gap:8px;margin-bottom:8px}.yaml-editor-heading{display:flex;align-items:center;flex-wrap:wrap;gap:8px;min-width:0}.yaml-editor-actions{display:flex;align-items:center;gap:8px;margin-left:auto;flex-shrink:0}.yaml-editor-fullscreen-host{height:100%;min-height:0}.yaml-editor-fullscreen-host .yaml-code-editor{height:100%}.yaml-editor-fullscreen-host :deep(.cm-editor){height:100%}.yaml-editor-fullscreen-host :deep(.cm-scroller){min-height:0;max-height:none;height:100%}:global(.yaml-editor-fullscreen-modal .ant-modal-content){height:100vh;height:100dvh;display:flex;flex-direction:column;border-radius:0}:global(.yaml-editor-fullscreen-modal .ant-modal-body){flex:1;min-height:0}
</style>
