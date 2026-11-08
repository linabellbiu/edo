import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'

export interface GitHubLogoSettings { enabled: boolean; version: number }

export const useInterfaceSettingsStore = defineStore('interface-settings', () => {
  const githubLogo = ref<GitHubLogoSettings | null>(null), error = ref('')
  const showGitHubLogo = computed(() => githubLogo.value?.enabled === true)
  let revision = 0, controller: AbortController | undefined
  let pending: Promise<GitHubLogoSettings | undefined> | undefined
  function validate(value: GitHubLogoSettings) {
    if (typeof value?.enabled !== 'boolean' || !Number.isSafeInteger(value.version) || value.version < 0) throw new Error('界面设置响应无效')
    return { enabled: value.enabled, version: value.version }
  }
  function apply(value: GitHubLogoSettings) {
    const saved = validate(value)
    revision++; controller?.abort(); pending = undefined; githubLogo.value = saved; error.value = ''
  }
  function reset() { revision++; controller?.abort(); pending = undefined; githubLogo.value = null; error.value = '' }
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
  return { githubLogo, showGitHubLogo, error, load, apply, reset }
})
