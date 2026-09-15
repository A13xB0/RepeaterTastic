<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, enc } from '@/api/client'
import type { Identity } from '@/api/types'
import Modal from '@/components/ui/Modal.vue'
import Toggle from '@/components/ui/Toggle.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { live, upsertIdentity } from '@/store/live'
import { toast } from '@/composables/toast'

const props = defineProps<{ identity: Identity | null }>()
const emit = defineEmits<{ close: [] }>()

const form = ref({ long_name: '', short_name: '', role: 'CLIENT_MUTE', api_port: 0, enabled: true, share_limit_pct: 25, hop_limit: 0,
  own_position: false, latitude: 0, longitude: 0, altitude: 0, position_secs: 0 })
const saving = ref(false)
const error = ref('')
const roles = ['CLIENT', 'CLIENT_MUTE', 'CLIENT_HIDDEN', 'TRACKER', 'SENSOR', 'ROUTER', 'ROUTER_LATE']

watch(
  () => props.identity,
  (i) => {
    if (!i) return
    error.value = ''
    form.value = { long_name: i.long_name, short_name: i.short_name, role: i.role, api_port: i.api?.port ?? 0, enabled: i.enabled, share_limit_pct: i.share_limit_pct ?? 25, hop_limit: i.hop_limit ?? 0,
      own_position: !!i.position, latitude: i.position?.latitude ?? 0, longitude: i.position?.longitude ?? 0, altitude: i.position?.altitude ?? 0,
      position_secs: i.position_secs ?? 0 }
  },
)

async function save() {
  const i = props.identity
  if (!i) return
  const f = form.value
  if (i.api && live.identities.some((x) => x.node_id !== i.node_id && x.api?.port === f.api_port)) {
    error.value = `Port ${f.api_port} is used by another identity`
    return
  }
  const patch: Record<string, unknown> = {}
  if (f.long_name !== i.long_name) patch.long_name = f.long_name.trim()
  if (f.short_name !== i.short_name) patch.short_name = f.short_name.trim()
  if (f.role !== i.role) patch.role = f.role
  if (f.enabled !== i.enabled) patch.enabled = f.enabled
  if (i.api && f.api_port !== i.api.port) patch.api_port = f.api_port
  if (f.share_limit_pct !== (i.share_limit_pct ?? 25)) patch.share_limit_pct = f.share_limit_pct
  if (f.hop_limit !== (i.hop_limit ?? 0)) patch.hop_limit = f.hop_limit
  if (f.position_secs !== (i.position_secs ?? 0)) patch.position_secs = f.position_secs
  const pos = f.own_position ? { latitude: f.latitude, longitude: f.longitude, altitude: f.altitude } : null
  if (JSON.stringify(pos) !== JSON.stringify(i.position ?? null)) patch.position = pos
  if (!Object.keys(patch).length) return emit('close')
  saving.value = true
  error.value = ''
  try {
    upsertIdentity(await api.patch<Identity>(`/identities/${enc(i.node_id)}`, patch))
    toast('Identity updated')
    emit('close')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Modal :open="!!identity" :title="`Edit ${identity?.long_name ?? ''}`" :subtitle="identity?.node_id" @close="emit('close')">
    <div v-if="identity" class="grid gap-4 sm:grid-cols-[1fr_7rem]">
      <div>
        <label class="label" for="e-ln">Long name</label>
        <input id="e-ln" v-model="form.long_name" class="input" maxlength="39" />
      </div>
      <div>
        <label class="label" for="e-sn">Short name</label>
        <input id="e-sn" v-model="form.short_name" class="input mono uppercase" maxlength="4" />
      </div>
      <div>
        <label class="label" for="e-role">Device role</label>
        <select id="e-role" v-model="form.role" class="input" :disabled="identity.is_relay">
          <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
        </select>
      </div>
      <div v-if="identity.api">
        <label class="label" for="e-port">API port</label>
        <input id="e-port" v-model.number="form.api_port" type="number" min="1024" max="65535" class="input tabular-nums" />
      </div>
      <div class="sm:col-span-2 rounded-xl border border-line-soft bg-raised px-3.5 py-3">
        <label class="flex items-center gap-2 text-[13px] font-medium">
          <input v-model="form.own_position" type="checkbox" class="size-4 accent-[var(--brand)]" /> Own fixed position
        </label>
        <p class="hint !mt-1">Otherwise it uses the radio's site position (if the radio broadcasts one from this identity). Also settable from the Meshtastic app.</p>
        <div v-if="form.own_position" class="mt-2 grid gap-3 sm:grid-cols-3">
          <div><label class="label" for="e-lat">Latitude</label><input id="e-lat" v-model.number="form.latitude" type="number" step="0.000001" class="input tabular-nums" /></div>
          <div><label class="label" for="e-lon">Longitude</label><input id="e-lon" v-model.number="form.longitude" type="number" step="0.000001" class="input tabular-nums" /></div>
          <div><label class="label" for="e-alt">Altitude (m)</label><input id="e-alt" v-model.number="form.altitude" type="number" class="input tabular-nums" /></div>
        </div>
        <div class="mt-2">
          <label class="label" for="e-psecs">Position broadcast</label>
          <select id="e-psecs" v-model.number="form.position_secs" class="input">
            <option :value="0">Radio default</option>
            <option v-for="v in [1800, 3600, 10800, 21600, 43200, 86400]" :key="v" :value="v">every {{ v >= 3600 ? `${v / 3600} h` : `${v / 60} min` }}</option>
          </select>
        </div>
      </div>
      <div class="sm:col-span-2">
        <label class="label" for="e-hops">Hop limit cap</label>
        <select id="e-hops" v-model.number="form.hop_limit" class="input">
          <option :value="0">Radio default</option>
          <option v-for="n in 7" :key="n" :value="n">{{ n }} hop{{ n === 1 ? '' : 's' }}</option>
        </select>
        <p class="hint">Every packet this identity sends is capped at this many hops, whatever its app or client asks for.</p>
      </div>
      <div v-if="!identity.is_relay" class="sm:col-span-2">
        <label class="label" for="e-share">Airtime share limit · {{ form.share_limit_pct }}% of the hourly duty budget</label>
        <input id="e-share" v-model.number="form.share_limit_pct" type="range" min="5" max="100" step="5" class="w-full accent-[var(--brand)]" />
        <p class="hint">An identity using more than this slice of the shared budget is flagged “Over share”.</p>
      </div>
      <div v-if="!identity.is_relay" class="flex items-center justify-between gap-3 rounded-xl border border-line-soft bg-raised px-3.5 py-3 sm:col-span-2">
        <div>
          <div class="text-[13px] font-medium">Enabled</div>
          <div class="text-xs text-ink-3">Disabled identities stop transmitting and close their API port.</div>
        </div>
        <Toggle v-model="form.enabled" label="Enabled" />
      </div>
    </div>
    <p v-if="error" class="mt-3 text-[13px] text-bad">{{ error }}</p>
    <template #footer>
      <button class="btn" @click="emit('close')">Cancel</button>
      <button class="btn btn-primary" :disabled="saving || !form.long_name.trim()" @click="save"><Spinner v-if="saving" />Save changes</button>
    </template>
  </Modal>
</template>
