<script setup lang="ts">
import DeploymentScriptEditor from '@/components/DeploymentScriptEditor.vue'
import type {PipelineBuildPlan} from '@/types/pipeline'
defineProps<{plan:PipelineBuildPlan;disabled:boolean;requireRegistry?:boolean;namedOutputs?:boolean;registries:Array<{id:string;name:string}>;imageDestination:'local'|'registry';saveArtifact:boolean;buildArgsText:string;environmentText:string;editPermission:string}>()
const emit=defineEmits<{change:[patch:Partial<PipelineBuildPlan>];'update:imageDestination':[value:'local'|'registry'];'update:saveArtifact':[value:boolean];'update:buildArgsText':[value:string];'update:environmentText':[value:string]}>()
</script>
<template>
 <section class="build-plan-fields">
  <a-form-item label="构建方式" required><a-select :value="plan.kind" :disabled="disabled" :options="[{value:'dockerfile',label:'镜像构建'},{value:'script',label:'自定义构建'}]" @update:value="emit('change',{kind:$event})"/></a-form-item>
  <template v-if="plan.kind==='dockerfile'">
   <a-form-item v-if="!requireRegistry" label="镜像输出"><a-radio-group :value="imageDestination" :disabled="disabled" @update:value="emit('update:imageDestination',$event)"><a-radio-button value="local">构建运行时本地镜像</a-radio-button><a-radio-button value="registry">推送镜像仓库</a-radio-button></a-radio-group></a-form-item>
   <a-form-item v-if="requireRegistry||imageDestination==='registry'" label="关联镜像仓库" required :validate-status="!plan.image_registry_id?'error':undefined" :help="!plan.image_registry_id?'请选择镜像仓库':undefined"><a-select :value="plan.image_registry_id||undefined" :disabled="disabled" :options="registries.map(v=>({value:v.id,label:v.name}))" placeholder="选择镜像仓库" @update:value="emit('change',{image_registry_id:String($event||'')})"/></a-form-item>
   <a-form-item required><template #label><span>Dockerfile 路径 <a-tooltip title="相对于本次运行固定 Commit 的仓库根目录，例如 docker/Dockerfile。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="Dockerfile 路径说明">?</button></a-tooltip></span></template><a-input :value="plan.dockerfile_path" :disabled="disabled" placeholder="Dockerfile" @update:value="emit('change',{dockerfile_path:String($event)})"/></a-form-item>
   <a-form-item required><template #label><span>Context <a-tooltip title="Docker 构建上下文，相对于仓库根目录；点号表示整个仓库。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="Context 说明">?</button></a-tooltip></span></template><a-input :value="plan.context_path" :disabled="disabled" placeholder="." @update:value="emit('change',{context_path:String($event)})"/></a-form-item>
   <a-form-item label="拉取基础镜像"><a-switch :checked="plan.pull!==false" :disabled="disabled" @change="emit('change',{pull:Boolean($event)})"/></a-form-item>
   <a-form-item label="构建缓存"><a-switch :checked="plan.cache_enabled!==false" :disabled="disabled" @change="emit('change',{cache_enabled:Boolean($event)})"/></a-form-item>
   <a-form-item label="构建参数（每行 KEY=value）"><a-textarea :value="buildArgsText" :disabled="disabled" :rows="3" placeholder="NODE_ENV=production" @update:value="emit('update:buildArgsText',String($event))"/></a-form-item>
  </template>
  <template v-else>
   <a-form-item required><DeploymentScriptEditor :model-value="plan.script||''" label="构建脚本" :example-text="'set -eu\n# 例如：npm ci && npm run build\n'" kind="script" :disabled="disabled" :edit-permission="editPermission" @update:model-value="emit('change',{script:$event})"/></a-form-item>
   <a-form-item v-if="!namedOutputs" label="保存产物"><a-switch :checked="saveArtifact" :disabled="disabled" @change="emit('update:saveArtifact',Boolean($event))"/></a-form-item>
   <a-form-item v-if="saveArtifact&&!namedOutputs" label="产物路径" required><a-input :value="plan.artifact_path" :disabled="disabled" placeholder="例如 dist 或 bin/server" @update:value="emit('change',{artifact_path:String($event)})"/></a-form-item>
   <a-form-item label="环境变量（每行 KEY=value）"><a-textarea :value="environmentText" :disabled="disabled" :rows="3" placeholder="NODE_ENV=production" @update:value="emit('update:environmentText',String($event))"/></a-form-item>
  </template>
  <a-form-item label="构建超时（秒）" required><a-input-number :value="plan.timeout_seconds" :min="30" :max="7200" :disabled="disabled" @update:value="emit('change',{timeout_seconds:Number($event)})"/></a-form-item>
 </section>
</template>
<style scoped>.build-plan-fields{display:grid;gap:12px;min-width:0}.build-plan-fields :deep(.ant-form-item){margin-bottom:0}.field-help{width:20px;height:20px;padding:0;border:1px solid var(--edo-border);border-radius:50%;background:transparent;color:var(--edo-muted);cursor:help}.field-help:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}</style>
