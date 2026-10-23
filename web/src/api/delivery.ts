import client from './client'
import type { WorkflowNode, WorkflowStage } from '@/types/pipeline'
export type DeliveryKind = 'docker'|'compose'|'kubernetes'|'script'
export const deliveryKinds: DeliveryKind[] = ['docker','compose','kubernetes','script']
export interface DeliveryPlan {id:string;name:string;kind:DeliveryKind;is_active:boolean;service_name?:string;deployment_target_id?:string}
export interface DeliveryTemplate {id:string;name:string;is_active:boolean;stages:WorkflowStage[]}
export interface DeliveryDefault {kind:DeliveryKind;version:number;workflow_template_id:string;deployment_plan_id:string;workflow_available:boolean;deployment_available:boolean;builtin:{name:string;source:WorkflowNode;stages:WorkflowStage[];deployment:DeliveryPlan}}
export async function getDeliveryDefaults(){return (await client.get<{defaults:DeliveryDefault[]}>('/delivery-defaults')).data.defaults}
export function oneDeliveryTask(template:DeliveryTemplate){const tasks=template.stages.flatMap(stage=>stage.tasks);return tasks.filter(task=>task.type==='build').length===1&&tasks.filter(task=>task.type==='deploy').length===1}
