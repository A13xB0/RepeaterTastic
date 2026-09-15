<script setup lang="ts">
import { ref, watch } from 'vue'
import { KeyRound, ShieldAlert } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Identity } from '@/api/types'
import Modal from '@/components/ui/Modal.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import Spinner from '@/components/ui/Spinner.vue'

const props = defineProps<{ identity: Identity | null }>()
const emit = defineEmits<{ close: [] }>()

const keys = ref<{ private_key: string; public_key: string } | null>(null)
const loading = ref(false)
const error = ref('')

watch(
  () => props.identity,
  () => {
    keys.value = null
    error.value = ''
  },
)

async function reveal() {
  if (!props.identity) return
  loading.value = true
  try {
    keys.value = await api.get(`/identities/${enc(props.identity.node_id)}/key`)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <Modal :open="!!identity" :title="`Keys · ${identity?.long_name ?? ''}`" :subtitle="identity?.node_id" @close="emit('close')">
    <div v-if="identity">
      <label class="label">Public key</label>
      <div class="flex items-center gap-1 rounded-xl border border-line-soft bg-raised px-3 py-2">
        <span class="mono min-w-0 flex-1 break-all">{{ identity.public_key }}</span>
        <CopyButton :text="identity.public_key" label="Public key" />
      </div>

      <label class="label mt-4">Private key</label>
      <div v-if="!keys" class="rounded-xl border border-warn/30 bg-warn/8 p-4">
        <div class="flex gap-3">
          <ShieldAlert class="size-5 shrink-0 text-warn" />
          <div class="text-[13px] leading-relaxed">
            <p class="font-medium">Anyone with this key can impersonate {{ identity.long_name }} and read its direct messages.</p>
            <p class="mt-1 text-ink-2">Only reveal it on a screen you trust, e.g. to move the node to a physical device or into a backup.</p>
          </div>
        </div>
        <button class="btn btn-sm mt-3" :disabled="loading" @click="reveal"><Spinner v-if="loading" /><KeyRound v-else class="size-3.5" />Reveal private key</button>
        <p v-if="error" class="mt-2 text-xs text-bad">{{ error }}</p>
      </div>
      <div v-else class="flex items-center gap-1 rounded-xl border border-warn/40 bg-warn/8 px-3 py-2">
        <span class="mono min-w-0 flex-1 break-all">{{ keys.private_key }}</span>
        <CopyButton :text="keys.private_key" label="Private key" />
      </div>
    </div>
    <template #footer>
      <button class="btn" @click="emit('close')">Done</button>
    </template>
  </Modal>
</template>
