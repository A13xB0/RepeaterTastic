<script setup lang="ts">
// Register a plugin that runs somewhere else (another container or machine) and show its token once.
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { Plugin } from '@/api/types'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import CopyButton from '@/components/ui/CopyButton.vue'

const props = defineProps<{ open: boolean; permissions: Record<string, string>; address?: string }>()
const emit = defineEmits<{ close: []; attached: [plugin: Plugin] }>()

const id = ref('')
const name = ref('')
const granted = ref<string[]>([])
const busy = ref(false)
const error = ref('')
const result = ref<{ plugin: Plugin; token: string; address: string } | null>(null)

watch(
  () => props.open,
  (o) => {
    if (!o) return
    id.value = name.value = error.value = ''
    granted.value = []
    result.value = null
  },
)
const idOk = computed(() => /^[a-z0-9][a-z0-9-]{1,39}$/.test(id.value))
const env = computed(() =>
  result.value ? `RT_PLUGIN_ID=${result.value.plugin.id}\nRT_PLUGIN_ADDR=${result.value.address || '<host>:<port>'}\nRT_PLUGIN_TOKEN=${result.value.token}` : '',
)

async function attach() {
  busy.value = true
  error.value = ''
  try {
    result.value = await api.post('/plugins/attach', { id: id.value, name: name.value.trim(), permissions: granted.value })
    emit('attached', result.value!.plugin)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="open" title="Attach a plugin" subtitle="For a plugin you run yourself, in another container or on another machine." size="md" @close="emit('close')">
    <div v-if="!address" class="mb-4 rounded-xl bg-warn/10 px-3 py-2.5 text-[13px] text-warn">
      Attaching is off. Set <span class="mono">plugins.listen</span> in the config file (for example <span class="mono">127.0.0.1:4450</span>) and restart.
    </div>

    <template v-if="!result">
      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <label class="label" for="at-id">Plugin ID</label>
          <input id="at-id" v-model="id" class="input mono" placeholder="my-plugin" maxlength="40" />
          <p class="hint" :class="id && !idOk ? '!text-bad' : ''">The id the plugin says in its Hello.</p>
        </div>
        <div>
          <label class="label" for="at-name">Name</label>
          <input id="at-name" v-model="name" class="input" placeholder="Plugin name" maxlength="60" />
        </div>
      </div>
      <div class="eyebrow mb-1 mt-4">Permissions</div>
      <label v-for="(text, key) in permissions" :key="key" class="flex cursor-pointer items-start gap-3 py-1.5">
        <input v-model="granted" type="checkbox" :value="key" class="mt-0.5 size-4 accent-[var(--brand)]" />
        <span class="text-[13px]">{{ text }} <span class="mono text-2xs text-ink-3">{{ key }}</span></span>
      </label>
    </template>

    <template v-else>
      <p class="text-[13px]">Give the plugin these settings. <strong>The token is shown only now.</strong> A new one can be made from the plugin's page.</p>
      <div class="relative mt-3">
        <pre class="mono overflow-x-auto rounded-lg bg-raised px-3 py-2.5 pr-10 text-xs leading-relaxed">{{ env }}</pre>
        <span class="absolute right-2 top-2"><CopyButton :text="env" label="Settings" /></span>
      </div>
      <p class="mt-3 text-xs text-ink-3">The connection isn't encrypted: keep it on localhost, a private network or a VPN.</p>
    </template>

    <p v-if="error" class="mt-3 text-[13px] text-bad">{{ error }}</p>
    <template #footer>
      <button class="btn" @click="emit('close')">{{ result ? 'Done' : 'Cancel' }}</button>
      <button v-if="!result" class="btn btn-primary" :disabled="busy || !idOk" @click="attach"><Spinner v-if="busy" />Attach</button>
    </template>
  </Modal>
</template>
