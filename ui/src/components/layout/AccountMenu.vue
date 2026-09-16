<script setup lang="ts">
// The admin account: change the password, sign out here, or sign every browser out.
import { onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { KeyRound, LogOut, MonitorX, UserRound } from '@lucide/vue'
import { api, setToken } from '@/api/client'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'

const router = useRouter()
const open = ref(false)
const pwOpen = ref(false)
const pw = ref({ current: '', next: '', repeat: '' })
const busy = ref(false)
const error = ref('')
const root = ref<HTMLElement | null>(null)

function onDocClick(e: MouseEvent) {
  if (open.value && root.value && !root.value.contains(e.target as Node)) open.value = false
}
document.addEventListener('click', onDocClick)
onBeforeUnmount(() => document.removeEventListener('click', onDocClick))

function signOut() {
  open.value = false
  setToken(null)
  router.push('/login')
}

async function signOutEverywhere() {
  open.value = false
  const ok = await confirmDialog({
    title: 'Sign out everywhere?',
    body: 'Every browser signed in to this RepeaterTastic, this one included, has to sign in again. API tokens keep working.',
    confirm: 'Sign out everywhere',
    danger: true,
  })
  if (!ok) return
  try {
    await api.post('/auth/logout-all')
  } catch (e) {
    toastError(e)
    return
  }
  setToken(null)
  router.push('/login')
}

function openPassword() {
  open.value = false
  pw.value = { current: '', next: '', repeat: '' }
  error.value = ''
  pwOpen.value = true
}

async function changePassword() {
  error.value = ''
  if (pw.value.next.length < 8) return (error.value = 'The new password needs at least 8 characters.')
  if (pw.value.next !== pw.value.repeat) return (error.value = "The new passwords don't match.")
  busy.value = true
  try {
    const r = await api.put<{ token: string }>('/auth/password', { current: pw.value.current, new: pw.value.next })
    if (r?.token) setToken(r.token)
    pwOpen.value = false
    toast('Password changed. Other browsers have been signed out.')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div ref="root" class="relative">
    <button type="button" class="icon-btn" title="Account" aria-label="Account" :aria-expanded="open" aria-haspopup="menu" @click.stop="open = !open">
      <UserRound class="size-4" />
    </button>
    <div v-if="open" role="menu" class="card absolute right-0 top-full z-50 mt-2 w-56 overflow-hidden p-1 shadow-xl">
      <div class="px-3 py-2 text-2xs text-ink-3">Signed in as <span class="font-medium text-ink-2">admin</span></div>
      <button type="button" role="menuitem" class="flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-left text-[13px] hover:bg-raised" @click="openPassword">
        <KeyRound class="size-4 text-ink-3" />Change password
      </button>
      <button type="button" role="menuitem" class="flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-left text-[13px] hover:bg-raised" @click="signOut">
        <LogOut class="size-4 text-ink-3" />Sign out
      </button>
      <button type="button" role="menuitem" class="flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-left text-[13px] text-bad hover:bg-raised" @click="signOutEverywhere">
        <MonitorX class="size-4" />Sign out everywhere
      </button>
    </div>

    <Modal :open="pwOpen" title="Change password" subtitle="Other browsers are signed out; you stay signed in here." size="sm" @close="pwOpen = false">
      <form class="grid gap-3" @submit.prevent="changePassword">
        <div>
          <label class="label" for="acct-cur">Current password</label>
          <input id="acct-cur" v-model="pw.current" type="password" class="input" autocomplete="current-password" autofocus />
        </div>
        <div>
          <label class="label" for="acct-new">New password</label>
          <input id="acct-new" v-model="pw.next" type="password" class="input" autocomplete="new-password" />
        </div>
        <div>
          <label class="label" for="acct-rep">Repeat new password</label>
          <input id="acct-rep" v-model="pw.repeat" type="password" class="input" autocomplete="new-password" />
        </div>
        <p v-if="error" class="hint !text-bad">{{ error }}</p>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn" @click="pwOpen = false">Cancel</button>
          <button type="submit" class="btn btn-primary" :disabled="busy || !pw.current || !pw.next"><Spinner v-if="busy" />Change password</button>
        </div>
      </form>
    </Modal>
  </div>
</template>
