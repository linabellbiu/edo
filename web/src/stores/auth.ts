import axios from 'axios'
import { create } from 'zustand'

import {
  getCurrentUser,
  login as loginRequest,
  loginLDAP as loginLDAPRequest,
  logout as logoutRequest,
  type User,
} from '@/api/client'

interface AuthState {
  user: User | null
  loaded: boolean
  loadFailed: boolean
  ensureLoaded: () => Promise<void>
  login: (username: string, password: string) => Promise<void>
  loginLDAP: (providerID: string, username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

let loadPromise: Promise<void> | null = null

export const useAuthStore = create<AuthState>((set, get) => ({
  user: null,
  loaded: false,
  loadFailed: false,

  ensureLoaded: async () => {
    if (get().loaded) return
    if (loadPromise) return loadPromise

    loadPromise = (async () => {
      try {
        const user = await getCurrentUser()
        set({ user, loaded: true, loadFailed: false })
      } catch (error) {
        if (axios.isAxiosError(error) && error.response?.status === 401) {
          set({ user: null, loaded: true, loadFailed: false })
          return
        }
        set({ user: null, loaded: true, loadFailed: true })
      }
    })().finally(() => {
      loadPromise = null
    })

    return loadPromise
  },

  login: async (username, password) => {
    const user = await loginRequest(username, password)
    set({ user, loaded: true, loadFailed: false })
  },

  loginLDAP: async (providerID, username, password) => {
    const user = await loginLDAPRequest(providerID, username, password)
    set({ user, loaded: true, loadFailed: false })
  },

  logout: async () => {
    try {
      await logoutRequest()
    } finally {
      set({ user: null, loaded: true, loadFailed: false })
    }
  },
}))
