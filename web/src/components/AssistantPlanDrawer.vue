<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Check, ShieldCheck } from 'lucide-vue-next'
import type { AssistantAction } from '@/api/assistant'
import YamlCodeEditor from '@/components/YamlCodeEditor.vue'

defineProps<{ action?: AssistantAction; busy: boolean; canConfirm: boolean; confirming: boolean; rejecting: boolean }>()
const emit = defineEmits<{ close: []; confirm: []; reject: [] }>()
const { t } = useI18n()
</script>

<template>
  <a-drawer :open="!!action" :title="action?.title || t('assistant.preview')" width="min(760px, 100vw)" :closable="!busy" :mask-closable="!busy" :keyboard="!busy" @close="emit('close')">
    <template v-if="action?.preview">
      <div class="assistant-plan-target"><ShieldCheck :size="18" /><strong>{{ action.target_name }}</strong><a-tag>{{ t(action.kind === 'yaml_draft' ? 'assistant.draftOnly' : 'assistant.action') }}</a-tag></div>
      <section class="assistant-plan-section"><h4>{{ t('assistant.changes') }}</h4><ul><li v-for="(item, i) in action.preview.changes" :key="i">{{ item }}</li></ul></section>
      <a-alert v-for="(item, i) in action.preview.warnings" :key="i" type="warning" show-icon :message="item" class="assistant-plan-warning" />
      <section class="assistant-plan-section"><h4>{{ t('assistant.checks') }}</h4><div v-for="(item, i) in action.preview.checks" :key="i" class="assistant-plan-check"><Check :size="14" />{{ item }}</div></section>
      <section v-if="action.preview.yaml" class="assistant-plan-section"><h4>{{ t('assistant.parameters') }}</h4><YamlCodeEditor :model-value="action.preview.yaml" readonly :issues="[]" :label="t('assistant.parameters')" /></section>
      <p v-if="action.result?.message">{{ action.result.message }}</p>
    </template>
    <template #footer><div class="assistant-plan-footer"><a-button :disabled="busy" @click="emit('close')">{{ t('assistant.close') }}</a-button><template v-if="canConfirm && action?.kind !== 'yaml_draft'"><a-button :loading="rejecting" :disabled="busy && !rejecting" @click="emit('reject')">{{ t('assistant.reject') }}</a-button><a-button type="primary" :loading="confirming" :disabled="busy && !confirming" @click="emit('confirm')">{{ t('assistant.confirm') }}</a-button></template></div></template>
  </a-drawer>
</template>

<style scoped>
.assistant-plan-target{display:flex;align-items:center;gap:8px;flex-wrap:wrap;color:var(--edo-text)}.assistant-plan-target>svg{color:var(--edo-primary)}.assistant-plan-target .ant-tag{margin-left:auto}.assistant-plan-section{margin-top:20px;color:var(--edo-text)}h4{margin:0 0 10px;font-size:14px}ul{padding-left:20px;margin:0;line-height:1.8;overflow-wrap:anywhere}.assistant-plan-warning{margin-top:14px}.assistant-plan-check{display:flex;align-items:center;gap:7px;margin:7px 0;color:var(--edo-muted)}.assistant-plan-check svg{color:var(--edo-primary);flex-shrink:0}.assistant-plan-footer{display:flex;justify-content:flex-end;gap:8px}p{color:var(--edo-text);line-height:1.7}
</style>
