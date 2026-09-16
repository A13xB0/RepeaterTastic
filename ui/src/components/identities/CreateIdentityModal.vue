<script setup lang="ts">
// Create or import an identity. The key is previewed first so the node number and last-byte clash are visible before saving.
import { computed, ref, watch } from 'vue'
import { RefreshCw, TriangleAlert, CircleCheck } from '@lucide/vue'
import { api, radio } from '@/api/client'
import type { Identity, KeyPreview } from '@/api/types'
import { hostedIdentityRoles } from '@/lib/relay'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import RadioFields from '@/components/identities/RadioFields.vue'
import { live, nodeLabel, refreshAllIdentities, upsertIdentity } from '@/store/live'
import { toast } from '@/composables/toast'

const props = defineProps<{ open: boolean; mode: 'create' | 'import' }>()
const emit = defineEmits<{ close: [] }>()

const longName = ref('')
const shortName = ref('')
const shortTouched = ref(false)
const port = ref(4403)
// Only the relay persona repeats; other identities say so by default.
const role = ref('CLIENT_MUTE')
const radioId = ref(radio.value)
const tab = ref<'generate' | 'import'>('generate')
const importKey = ref('')
const preview = ref<KeyPreview | null>(null)
const previewError = ref('')
const previewing = ref(false)
const acceptClash = ref(false)
const saving = ref(false)
const error = ref('')

// Every new identity runs on meshtasticd, so its role can't repeat.
const roles = hostedIdentityRoles

function nextPort() {
  const used = new Set((live.allIdentities.length ? live.allIdentities : live.identities).map((i) => i.api?.port).filter(Boolean))
  let p = 4403
  while (used.has(p)) p++
  return p
}

watch(
  () => props.open,
  (o) => {
    if (!o) return
    refreshAllIdentities()
    longName.value = ''
    shortName.value = ''
    shortTouched.value = false
    port.value = nextPort()
    role.value = 'CLIENT_MUTE'
    radioId.value = radio.value
    tab.value = props.mode === 'import' ? 'import' : 'generate'
    importKey.value = ''
    preview.value = null
    error.value = ''
    acceptClash.value = false
    if (tab.value === 'generate') generate()
  },
)

watch(longName, (v) => {
  if (shortTouched.value) return
  const words = v.trim().split(/\s+/).filter(Boolean)
  shortName.value = (words.length > 1 ? words.map((w) => w[0]).join('') : v.replace(/\s/g, '')).slice(0, 4).toUpperCase()
})

async function runPreview(privateKey?: string) {
  previewing.value = true
  previewError.value = ''
  acceptClash.value = false
  try {
    preview.value = await api.post<KeyPreview>('/identities/preview-key', privateKey ? { private_key: privateKey } : {})
  } catch (e) {
    preview.value = null
    previewError.value = (e as Error).message
  } finally {
    previewing.value = false
  }
}
const generate = () => runPreview()

let debounce: number | undefined
watch([importKey, tab], () => {
  if (tab.value !== 'import') return
  clearTimeout(debounce)
  preview.value = null
  const k = importKey.value.trim()
  if (!k) return
  if (!/^[A-Za-z0-9+/]{42,43}=?$/.test(k)) {
    previewError.value = 'Expected a base64 X25519 private key (32 bytes, 44 characters)'
    return
  }
  previewError.value = ''
  debounce = window.setTimeout(() => runPreview(k), 300)
})
watch(tab, (t) => {
  if (t === 'generate') {
    previewError.value = ''
    generate()
  }
})

const idParts = computed(() => (preview.value ? { head: preview.value.node_id.slice(0, 7), tail: preview.value.node_id.slice(7) } : null))
const portClash = computed(() => (live.allIdentities.length ? live.allIdentities : live.identities).some((i) => i.api?.port === port.value))
const canSave = computed(
  () => !!longName.value.trim() && !!shortName.value.trim() && !!preview.value && !portClash.value && (!preview.value.collision || acceptClash.value),
)

async function save() {
  if (!preview.value || !canSave.value) return
  saving.value = true
  error.value = ''
  try {
    const ident = await api.post<Identity>('/identities', {
      long_name: longName.value.trim(),
      short_name: shortName.value.trim(),
      private_key: preview.value.private_key,
      api_port: port.value,
      role: role.value,
      radio_id: live.radios.length > 1 ? radioId.value : undefined,
    })
    upsertIdentity(ident)
    toast(`${ident.long_name} created as ${ident.node_id}`)
    emit('close')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Modal
    :open="open"
    :title="mode === 'import' ? 'Import identity' : 'New identity'"
    subtitle="Each identity is a full Meshtastic node with its own key, node number and client-API port."
    size="lg"
    @close="emit('close')"
  >
    <div class="grid gap-4 sm:grid-cols-[1fr_7rem]">
      <div>
        <label class="label" for="ln">Long name</label>
        <input id="ln" v-model="longName" class="input" maxlength="39" placeholder="e.g. Trail Beacon" autofocus />
      </div>
      <div>
        <label class="label" for="sn">Short name</label>
        <input id="sn" v-model="shortName" class="input mono uppercase" maxlength="4" @input="shortTouched = true" />
      </div>
      <div>
        <label class="label" for="role">Device role</label>
        <select id="role" v-model="role" class="input">
          <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
        </select>
        <p class="hint">Runs on meshtasticd, keeping the key RepeaterTastic generates.</p>
      </div>
      <RadioFields v-model:home="radioId" creating class="sm:col-span-2" />
      <div>
        <label class="label" for="port">API port</label>
        <input id="port" v-model.number="port" type="number" min="1024" max="65535" class="input tabular-nums" />
      </div>
    </div>
    <p v-if="portClash" class="hint !text-bad">Port {{ port }} is already used by another identity.</p>

    <div class="mt-5">
      <div class="seg">
        <button :aria-pressed="tab === 'generate'" @click="tab = 'generate'">Generate new key</button>
        <button :aria-pressed="tab === 'import'" @click="tab = 'import'">Import existing key</button>
      </div>
      <div v-if="tab === 'import'" class="mt-3">
        <label class="label" for="pk">Private key (base64)</label>
        <textarea id="pk" v-model="importKey" rows="2" class="input mono resize-none" placeholder="From a Meshtastic device's Security settings" spellcheck="false" />
        <p class="hint">Importing a key moves that node's identity here. Turn the original device off first or both will clash on air.</p>
      </div>
    </div>

    <div class="mt-4 rounded-xl border border-line-soft bg-raised p-4">
      <div class="flex items-center justify-between">
        <span class="eyebrow">Node number preview</span>
        <button v-if="tab === 'generate'" class="btn btn-sm btn-ghost" :disabled="previewing" @click="generate">
          <RefreshCw :class="['size-3.5', previewing && 'animate-spin']" />Regenerate
        </button>
      </div>
      <p v-if="previewError" class="mt-2 text-[13px] text-bad">{{ previewError }}</p>
      <div v-else-if="preview && idParts" class="mt-2">
        <div class="flex flex-wrap items-baseline gap-x-5 gap-y-1">
          <div class="mono text-[26px] font-semibold leading-none tracking-tight">
            {{ idParts.head }}<span :class="['rounded px-0.5', preview.collision ? 'bg-warn/20 text-warn' : 'bg-brand/15 text-brand']">{{ idParts.tail }}</span>
          </div>
          <div class="text-xs text-ink-3">node_num {{ preview.node_num }} · last byte 0x{{ preview.last_byte.toString(16).padStart(2, '0') }}</div>
        </div>
        <div class="mono mt-2 truncate text-2xs text-ink-3" :title="preview.public_key">public key {{ preview.public_key }}</div>
        <div v-if="preview.collision" class="mt-3 flex items-start gap-2 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-[13px]">
          <TriangleAlert class="mt-0.5 size-4 shrink-0 text-warn" />
          <div class="min-w-0">
            Last byte clashes with <b>{{ nodeLabel(preview.collision).long }}</b> <span class="mono">{{ preview.collision }}</span>.
            Next-hop routing and relay attribution use only this byte, so packets could be misattributed.
            <label v-if="tab === 'import'" class="mt-1.5 flex items-center gap-2 text-xs text-ink-2">
              <input v-model="acceptClash" type="checkbox" class="accent-[var(--warn)]" />Use this key anyway
            </label>
            <span v-else class="mt-1 block text-xs text-ink-2">Regenerate to pick a different key.</span>
          </div>
        </div>
        <div v-else class="mt-3 flex items-center gap-2 text-[13px] text-ok">
          <CircleCheck class="size-4" />Last byte is unique among local and heard nodes
        </div>
      </div>
      <div v-else class="mt-2 h-10 animate-pulse rounded-lg bg-sunken" />
    </div>
    <p v-if="error" class="mt-3 text-[13px] text-bad">{{ error }}</p>

    <template #footer>
      <button class="btn" @click="emit('close')">Cancel</button>
      <button class="btn btn-primary" :disabled="!canSave || saving" @click="save"><Spinner v-if="saving" />Create identity</button>
    </template>
  </Modal>
</template>
