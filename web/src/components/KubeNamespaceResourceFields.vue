<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Trash2 } from 'lucide-vue-next'
import KubeSelect from './KubeSelect.vue'
import { limitFields, limitTypes, type LimitRule, type NamespaceResourceDraft } from '@/utils/kubernetesNamespaceResources'

const draft = defineModel<NamespaceResourceDraft>({ required: true })
defineProps<{ disabled: boolean }>()
const { t } = useI18n()

function addLimitRule() {
  const form = draft.value
  if (form.kind !== 'LimitRange') return
  const type = limitTypes.find(type => form.rules.every(rule => rule.type !== type))
  if (type) form.rules.push({ type, resources: [{ name: '' }] })
}
function visibleFields(rule: LimitRule) {
  return limitFields.filter(field => rule.type === 'Container' || field === 'min' || field === 'max' ||
    rule.type === 'Pod' && field === 'maxLimitRequestRatio' || rule.resources.some(item => Boolean(item[field])))
}
</script>

<template>
  <section v-if="draft.kind === 'ResourceQuota'" class="namespace-policy-fields" :aria-label="t('k8s.quotaHard')">
    <h3>{{ t('k8s.quotaHard') }}</h3>
    <div v-for="(limit, index) in draft.limits" :key="index" class="quota-editor-row">
      <label><span>{{ t('k8s.resourceKey') }}</span><a-input v-model:value="limit.name" :maxlength="253" placeholder="requests.cpu" /></label>
      <label><span>{{ t('k8s.quotaHard') }}</span><a-input v-model:value="limit.amount" :maxlength="128" placeholder="2" /></label>
      <a-button type="text" danger :disabled="disabled" :aria-label="t('k8s.remove')" @click="draft.limits.splice(index, 1)"><Trash2 :size="14" aria-hidden="true" /></a-button>
    </div>
    <a-button class="namespace-add-field" size="small" :disabled="disabled" @click="draft.limits.push({ name: '', amount: '' })">+ {{ t('k8s.addQuotaLimit') }}</a-button>
  </section>
  <section v-else-if="draft.kind === 'LimitRange'" class="namespace-policy-fields" :aria-label="t('k8s.limitRules')">
    <h3>{{ t('k8s.limitRules') }}</h3>
    <fieldset v-for="(rule, index) in draft.rules" :key="index" class="limit-rule" :disabled="disabled">
      <legend class="namespace-sr-only">{{ t('k8s.limitRuleNumber', { count: index + 1 }) }}</legend>
      <div class="limit-rule-heading">
        <label>
          <span>{{ t('k8s.limitType') }}</span>
          <KubeSelect v-model:value="rule.type" :aria-label="t('k8s.limitType')" :disabled="disabled"
            :options="limitTypes.map(value => ({ value, label: t(`k8s.limitType_${value}`), disabled: draft.kind === 'LimitRange' && draft.rules.some((item, position) => position !== index && item.type === value) }))" />
        </label>
        <a-button type="text" danger :disabled="disabled" :aria-label="t('k8s.removeLimitRule')" @click="draft.rules.splice(index, 1)"><Trash2 :size="14" aria-hidden="true" /></a-button>
      </div>
      <div v-for="(resource, position) in rule.resources" :key="position" class="limit-resource-row">
        <label><span>{{ t('k8s.resourceKey') }}</span><a-input v-model:value="resource.name" :maxlength="253" :placeholder="rule.type === 'PersistentVolumeClaim' ? 'storage' : 'cpu'" /></label>
        <label v-for="field in visibleFields(rule)" :key="field"><span>{{ t(`k8s.limit_${field}`) }}</span><a-input v-model:value="resource[field]" :maxlength="128" /></label>
        <a-button type="text" danger :disabled="disabled" :aria-label="t('k8s.remove')" @click="rule.resources.splice(position, 1)"><Trash2 :size="14" aria-hidden="true" /></a-button>
      </div>
      <a-button class="namespace-add-field" size="small" :disabled="disabled" @click="rule.resources.push({ name: '' })">+ {{ t('k8s.addLimitResource') }}</a-button>
    </fieldset>
    <a-button v-if="draft.rules.length < limitTypes.length" class="namespace-add-field" size="small" :disabled="disabled" @click="addLimitRule">+ {{ t('k8s.addLimitRule') }}</a-button>
  </section>
  <section v-else class="namespace-policy-fields" :aria-label="t('k8s.serviceAccountSettings')">
    <h3>{{ t('k8s.serviceAccountSettings') }}</h3>
    <div class="account-fields">
      <a-form-item :label="t('k8s.serviceAccountAutomount')">
        <KubeSelect v-model:value="draft.automount" :aria-label="t('k8s.serviceAccountAutomount')" :disabled="disabled" :options="['default','enabled','disabled'].map(value => ({ value, label: t(`k8s.automount_${value}`) }))" />
      </a-form-item>
      <a-form-item :label="t('k8s.imagePullSecretReferences')">
        <a-input v-model:value="draft.imagePullSecrets" :aria-label="t('k8s.imagePullSecretReferences')" placeholder="registry-auth" />
      </a-form-item>
    </div>
  </section>
</template>

<style scoped>
.namespace-policy-fields { display: grid; gap: 10px; margin-block: 14px; min-width: 0; }
h3 { border-bottom: 1px solid var(--edo-border); padding-bottom: 8px; margin: 0; color: var(--edo-text); font-size: 13px; font-weight: 600; }
.quota-editor-row, .limit-rule-heading, .limit-resource-row { display: flex; align-items: end; flex-wrap: wrap; gap: 6px; }
.quota-editor-row label, .limit-rule-heading label, .limit-resource-row label { display: grid; gap: 4px; min-width: 0; color: var(--edo-muted); font-size: 12px; }
.quota-editor-row label:first-child { flex: 0 1 240px; }
.quota-editor-row label:nth-child(2) { flex: 0 1 128px; }
.limit-rule { display: grid; gap: 8px; min-width: 0; border: 1px solid var(--edo-border); border-radius: 6px; padding: 10px; margin: 0; }
.limit-rule-heading { justify-content: space-between; }
.limit-rule-heading label { width: min(100%, 200px); }
.limit-resource-row label { flex: 1 0 78px; }
.limit-resource-row label:first-child { flex: 2 0 120px; }
.namespace-add-field { justify-self: start; }
.account-fields { display: grid; gap: 10px; grid-template-columns: minmax(150px, 210px) minmax(0, 1fr); }
.account-fields :deep(.ant-form-item) { margin-bottom: 0; }
.namespace-sr-only { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
@media (max-width: 560px) {
  .quota-editor-row label:first-child { flex: 2 1 92px; }
  .quota-editor-row label:nth-child(2) { flex: 1 1 65px; }
  .account-fields { grid-template-columns: 1fr; }
}
</style>
