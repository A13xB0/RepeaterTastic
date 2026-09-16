<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Eye, EyeOff, Monitor, Moon, Sun } from '@lucide/vue'
import { request, setToken } from '@/api/client'
import Logo from '@/components/ui/Logo.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { cycleTheme, themeMode } from '@/composables/theme'

const router = useRouter()
const route = useRoute()
const password = ref('')
const show = ref(false)
const error = ref('')
const busy = ref(false)

async function submit() {
  if (!password.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const r = await request<{ token: string }>('POST', '/auth/login', { password: password.value }, { auth: false })
    setToken(r.token)
    const next = typeof route.query.next === 'string' && route.query.next.startsWith('/') ? route.query.next : '/'
    router.replace(next)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="app-bg flex min-h-dvh flex-col items-center justify-center px-4 py-10">
    <button type="button" class="icon-btn fixed right-4 top-4" :title="`Theme: ${themeMode}`" @click="cycleTheme">
      <Sun v-if="themeMode === 'light'" class="size-4" /><Moon v-else-if="themeMode === 'dark'" class="size-4" /><Monitor v-else class="size-4" />
    </button>
    <div class="card w-full max-w-sm !bg-surface-solid/90 px-6 pb-6 pt-8 sm:px-8">
      <div class="flex flex-col items-center text-center">
        <Logo :size="56" />
        <h1 class="mt-4 text-xl font-bold tracking-tight">Repeater<span class="text-brand">Tastic</span></h1>
        <p class="mt-1 text-[13px] text-ink-3">Sign in to manage your virtual Meshtastic nodes</p>
      </div>
      <form class="mt-7 space-y-4" @submit.prevent="submit">
        <div>
          <label class="label" for="pw">Admin password</label>
          <div class="relative">
            <input id="pw" v-model="password" :type="show ? 'text' : 'password'" class="input pr-10" autocomplete="current-password" autofocus />
            <button type="button" class="icon-btn absolute right-0.5 top-0.5" :aria-label="show ? 'Hide password' : 'Show password'" @click="show = !show">
              <EyeOff v-if="show" class="size-4" /><Eye v-else class="size-4" />
            </button>
          </div>
          <p v-if="error" class="mt-2 text-xs text-bad">{{ error }}</p>
        </div>
        <button type="submit" class="btn btn-primary w-full" :disabled="!password || busy">
          <Spinner v-if="busy" />Sign in
        </button>
      </form>
    </div>
    <p class="mt-6 text-xs text-ink-3">Scripts and Home Assistant should use an API token instead.</p>
  </div>
</template>
