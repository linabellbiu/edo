import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'

export interface GitHubLogoSettings { enabled: boolean; version: number }
export interface DeliveryControlsMenuSettings { enabled: boolean; version: number }

export const useInterfaceSettingsStore = defineStore('interface-settings', () => {
  const githubLogo = ref<GitHubLogoSettings | null>(null), error = ref('')
  const deliveryControlsMenu = ref<DeliveryControlsMenuSettings | null>(null), deliveryError = ref('')
  const showGitHubLogo = computed(() => githubLogo.value?.enabled === true)
  // 应用数据库与主机组保护入口默认隐藏，只有管理员的界面设置明确开启后才展示。
  const showDeliveryControls = computed(() => deliveryControlsMenu.value?.enabled === true)
  let revision = 0, controller: AbortController | undefined
  let pending: Promise<GitHubLogoSettings | undefined> | undefined
  let deliveryRevision = 0, deliveryController: AbortController | undefined
  let deliveryPending: Promise<DeliveryControlsMenuSettings | undefined> | undefined
  function validate(value: GitHubLogoSettings) {
    if (typeof value?.enabled !== 'boolean' || !Number.isSafeInteger(value.version) || value.version < 0) throw new Error('界面设置响应无效')
    return { enabled: value.enabled, version: value.version }
  }
  function apply(value: GitHubLogoSettings) {
    const saved = validate(value)
    revision++; controller?.abort(); pending = undefined; githubLogo.value = saved; error.value = ''
  }

  function applyDeliveryControls(value: DeliveryControlsMenuSettings) {
    const saved = validate(value)
    deliveryRevision++; deliveryController?.abort(); deliveryPending = undefined; deliveryControlsMenu.value = saved; deliveryError.value = ''
  }
  function loadDeliveryControls() {
    if (deliveryPending) return deliveryPending
    const request = readDelivery()
    deliveryPending = request
    const clear = () => { if (deliveryPending === request) deliveryPending = undefined }
    void request.then(clear, clear)
    return request
  }
  async function readDelivery() {
    deliveryController?.abort(); deliveryController = new AbortController()
    const request = ++deliveryRevision, signal = deliveryController.signal
    deliveryError.value = ''
    try {
      const { data } = await client.get<DeliveryControlsMenuSettings>('/settings/delivery-controls-menu', { signal })
      if (request !== deliveryRevision || signal.aborted) return
      deliveryControlsMenu.value = validate(data)
      return deliveryControlsMenu.value
    } catch (cause) { if (request === deliveryRevision && !signal.aborted) deliveryError.value = apiErrorMessage(cause) }
  }
  function reset() {
    revision++; controller?.abort(); pending = undefined; githubLogo.value = null; error.value = ''
    deliveryRevision++; deliveryController?.abort(); deliveryPending = undefined; deliveryControlsMenu.value = null; deliveryError.value = ''
  }
  function load() {
    if (pending) return pending
    const request = read()
    pending = request
    const clear = () => { if (pending === request) pending = undefined }
    void request.then(clear, clear)
    return request
  }
  async function read() {
    controller?.abort(); controller = new AbortController()
    const request = ++revision, signal = controller.signal
    error.value = ''
    try {
      const { data } = await client.get<GitHubLogoSettings>('/settings/github-logo', { signal })
      if (request !== revision || signal.aborted) return
      githubLogo.value = validate(data)
      return githubLogo.value
    } catch (cause) { if (request === revision && !signal.aborted) error.value = apiErrorMessage(cause) }
  }
  return { githubLogo, showGitHubLogo, error, load, apply, deliveryControlsMenu, showDeliveryControls, deliveryError, loadDeliveryControls, applyDeliveryControls, reset }
})
