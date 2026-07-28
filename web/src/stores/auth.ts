import axios from 'axios'
import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

import {
  getCurrentUser,
  login as loginRequest,
  loginLDAP as loginLDAPRequest,
  logout as logoutRequest,
  type User,
} from '@/api/client'

let loadPromise: Promise<void> | null = null

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const loaded = ref(false)
  const loadFailed = ref(false)
  const permissions = computed(() => new Set(user.value?.permissions ?? []))

  async function ensureLoaded() {
    if (loaded.value) return
    if (loadPromise) return loadPromise

    loadPromise = (async () => {
      try {
        user.value = await getCurrentUser()
        loaded.value = true
        loadFailed.value = false
      } catch (error) {
        if (axios.isAxiosError(error) && error.response?.status === 401) {
          user.value = null
          loaded.value = true
          loadFailed.value = false
          return
        }
        user.value = null
        loaded.value = true
        loadFailed.value = true
      }
    })().finally(() => {
      loadPromise = null
    })

    return loadPromise
  }

  async function login(username: string, password: string) {
    user.value = await loginRequest(username, password)
    loaded.value = true
    loadFailed.value = false
  }

  async function loginLDAP(providerID: string, username: string, password: string) {
    user.value = await loginLDAPRequest(providerID, username, password)
    loaded.value = true
    loadFailed.value = false
  }

  async function logout() {
    try {
      await logoutRequest()
    } finally {
      user.value = null
      loaded.value = true
      loadFailed.value = false
    }
  }

  function canAny(required: string[]) {
    if (user.value?.is_superuser || required.length === 0) return true
    return required.some((permission) => permissions.value.has(permission))
  }

  return { user, loaded, loadFailed, permissions, ensureLoaded, login, loginLDAP, logout, canAny }
})
