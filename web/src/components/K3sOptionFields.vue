<script setup lang="ts">
import { computed, ref } from 'vue'
import { Search } from 'lucide-vue-next'
import type { K3sOption } from '@/api/k3s'
import { k3sDefaultValue, k3sOptionDefaultText, k3sOptionGroups } from '@/utils/k3sProvision'

const props = defineProps<{ options: K3sOption[]; value: Record<string, unknown>; disabled?: boolean }>()
const emit = defineEmits<{ 'update:value': [value: Record<string, unknown>] }>()
const query = ref('')
const groups = computed(() => k3sOptionGroups(props.options, query.value))
const selectedCount = computed(() => props.options.filter(option => Object.hasOwn(props.value, option.name)).length)
const selected = (option: K3sOption) => Object.hasOwn(props.value, option.name)
function toggle(option: K3sOption, enabled: boolean) {
  if (props.disabled || option.managed || option.unsupported_reason) return
  const values = {...props.value}
  if (enabled) values[option.name] = k3sDefaultValue(option)
  else delete values[option.name]
  emit('update:value', values)
}
function update(option: K3sOption, value: unknown) {
  if (props.disabled || option.managed || option.unsupported_reason) return
  emit('update:value', {...props.value, [option.name]: value})
}
const stringList = (value: unknown) => Array.isArray(value) ? value.join('\n') : ''
</script>

<template>
  <div class="k3s-option-fields">
    <div class="option-search"><a-input v-model:value="query" allow-clear placeholder="搜索参数名称、用途或分类" aria-label="搜索 K3s 安装选项"><template #prefix><Search :size="15" /></template></a-input><span>{{ options.length }} 项 · 已设置 {{ selectedCount }} 项</span></div>
    <p class="option-help">勾选需要修改的参数，未勾选的参数使用 K3s 默认值。</p>
    <a-empty v-if="!groups.length" description="没有匹配的参数" />
    <details v-for="(group,index) in groups" :key="group.name" class="option-group" :open="Boolean(query) || index === 0">
      <summary>{{ group.name }}<span>{{ group.items.length }} 项</span></summary>
      <div v-for="option in group.items" :key="option.name" class="option-row">
        <div class="option-heading"><a-checkbox :checked="selected(option)" :disabled="disabled || option.managed || Boolean(option.unsupported_reason)" @change="toggle(option,$event.target.checked)"><strong>{{ option.label || option.name }}</strong></a-checkbox><code>{{ option.name.startsWith('INSTALL_') ? option.name : `--${option.name}` }}</code><a-tag v-if="option.managed">自动管理</a-tag></div>
        <p>{{ option.unsupported_reason || option.description }}</p>
        <span class="option-default">默认：{{ k3sOptionDefaultText(option) }}</span>
        <div v-if="selected(option) && !option.managed && !option.unsupported_reason" class="option-value">
          <a-switch v-if="option.type === 'boolean'" :checked="Boolean(value[option.name])" :disabled="disabled" checked-children="开启" un-checked-children="关闭" :aria-label="option.label || option.name" @update:checked="update(option,$event)" />
          <a-input-number v-else-if="option.type === 'integer'" :value="Number(value[option.name])" :precision="0" :disabled="disabled" :aria-label="option.label || option.name" @update:value="update(option,$event)" />
          <a-select v-else-if="option.choices?.length" :value="value[option.name]" :mode="option.type === 'string[]' ? 'multiple' : undefined" :options="option.choices.map(value=>({value,label:value}))" :disabled="disabled" :aria-label="option.label || option.name" @update:value="update(option,$event)" />
          <a-textarea v-else-if="option.type === 'string[]'" :value="stringList(value[option.name])" :rows="3" placeholder="每行一个值" :disabled="disabled" :aria-label="option.label || option.name" @update:value="update(option,$event.split('\n').map((item:string)=>item.trim()).filter(Boolean))" />
          <a-input-password v-else-if="option.secret" :value="String(value[option.name] ?? '')" autocomplete="new-password" :disabled="disabled" :aria-label="option.label || option.name" @update:value="update(option,$event)" />
          <a-input v-else :value="String(value[option.name] ?? '')" :disabled="disabled" :aria-label="option.label || option.name" @update:value="update(option,$event)" />
        </div>
      </div>
    </details>
  </div>
</template>

<style scoped>
.option-search{display:flex;align-items:center;gap:14px}.option-search>.ant-input-affix-wrapper{flex:1}.option-search>span{flex:none;color:var(--edo-muted);font-size:12px}.option-help{font-size:12px;color:var(--edo-muted);margin:10px 0 16px}.option-group{border:1px solid var(--edo-border);border-radius:6px;margin-bottom:10px;overflow:hidden}.option-group>summary{cursor:pointer;padding:12px 14px;font-size:13px;font-weight:600;background:var(--edo-bg)}.option-group>summary>span{margin-left:9px;font-size:11px;font-weight:400;color:var(--edo-muted)}.option-row{padding:13px 16px;border-top:1px solid var(--edo-border)}.option-heading{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.option-heading strong{font-weight:500;font-size:13px}.option-heading code{font-size:11px;color:var(--edo-muted);overflow-wrap:anywhere}.option-heading .ant-tag{margin-left:auto;font-size:11px}.option-row>p{margin:5px 0 4px;font-size:12px;line-height:1.65;color:var(--edo-muted)}.option-default{font-size:11px;color:var(--edo-muted)}.option-value{margin-top:10px}.option-value>.ant-select,.option-value>.ant-input-number{width:100%}@media(max-width:600px){.option-search{align-items:stretch;flex-direction:column;gap:6px}.option-row{padding:12px}.option-heading{gap:6px}}
</style>
