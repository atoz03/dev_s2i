import { ref } from 'vue'

export function useGeminiOAuth() {
  const authUrl = ref('')
  const sessionId = ref('')
  const state = ref('')
  const loading = ref(false)
  const error = ref('该功能已下线')

  return {
    authUrl,
    sessionId,
    state,
    loading,
    error,
    resetState: () => { authUrl.value = ''; sessionId.value = ''; state.value = ''; loading.value = false; error.value = '' },
    generateAuthUrl: async (..._args: unknown[]) => false,
    exchangeAuthCode: async (..._args: unknown[]) => null,
    validateRefreshToken: async (..._args: unknown[]) => null,
    buildCredentials: (tokenInfo: Record<string, unknown> = {}) => ({ ...tokenInfo }),
    buildExtraInfo: (tokenInfo: Record<string, unknown> = {}) => ({ ...tokenInfo }),
    getCapabilities: async () => ({ ai_studio_oauth_enabled: false })
  }
}
