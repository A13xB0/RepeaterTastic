<script setup lang="ts">
// The radios on this site: add, rename and remove them, and the airtime budget they share.
// Each radio's own settings (preset, relay, MQTT…) are edited with the radio switcher.
import { computed, onMounted, ref, watch } from 'vue'
import { Pencil, Plus, Radio as RadioIcon, Settings2, Trash, TriangleAlert } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Phy, RadiosResponse, Region, SerialPort } from '@/api/types'
import { refreshRadios } from '@/store/live'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import RadioSettingsModal from '@/components/config/RadioSettingsModal.vue'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'
import { num } from '@/lib/format'

const props = defineProps<{ ports: SerialPort[]; regions: Region[] }>()
const emit = defineEmits<{ restart: [] }>()

const data = ref<RadiosResponse | null>(null)
const site = ref<{ duty_cycle_percent: number; running_duty_cycle_percent: number; coordinator: boolean } | null>(null)
const siteDuty = ref(0)

async function load() {
  const [r, s] = await Promise.all([api.get<RadiosResponse>('/radios'), api.get<NonNullable<typeof site.value>>('/site')])
  data.value = r
  site.value = s
  siteDuty.value = s.duty_cycle_percent
  if (r.restart_required) emit('restart')
}
onMounted(() => load().catch(toastError))

const presetLabel = (p: string) => p.split('_').map((w) => w[0] + w.slice(1).toLowerCase()).join('')
const roleLabel: Record<string, string> = { mute: 'mute (never repeats)', client: 'client (repeats)', router: 'router', monitor: 'monitor (listens only)', off: 'off' }
const usedDevices = computed(() => new Set([...(data.value?.radios ?? []).map((r) => r.device), ...(data.value?.pending ?? []).map((p) => p.device)]))

// ---- rename
const renaming = ref<string | null>(null)
const newName = ref('')
function startRename(id: string, name: string) {
  renaming.value = id
  newName.value = name
}
async function saveName(id: string) {
  try {
    await api.patch(`/radios/${enc(id)}`, { name: newName.value })
    renaming.value = null
    await Promise.all([load(), refreshRadios()])
    toast('Renamed')
  } catch (e) {
    toastError(e)
  }
}

// ---- remove
async function remove(id: string, name: string, running: boolean) {
  const ok = await confirmDialog({
    title: `Remove ${name}?`,
    body: running
      ? 'It keeps running until the daemon restarts, then its identities go off air and their app connections stop. Its identity keys and history stay on disk, so adding a radio with the same ID brings them back.'
      : 'It was never started, so nothing goes off air.',
    confirm: 'Remove radio',
    danger: true,
  })
  if (!ok) return
  try {
    const r = await api.del<{ restart_required: boolean }>(`/radios/${enc(id)}`)
    if (r.restart_required) emit('restart')
    await load()
    toast(`${name} removed`)
  } catch (e) {
    toastError(e)
  }
}

// ---- site budget
const savingSite = ref(false)
async function saveSite() {
  savingSite.value = true
  try {
    const r = await api.put<{ restart_required: boolean }>('/site', { duty_cycle_percent: siteDuty.value })
    if (r.restart_required) emit('restart')
    await load()
    toast(siteDuty.value ? `Site airtime capped at ${siteDuty.value}%` : 'Site airtime cap off')
  } catch (e) {
    toastError(e)
  } finally {
    savingSite.value = false
  }
}

// ---- add
const adding = ref(false)
/** Set when the form edits a radio that hasn't started yet. */
const editingId = ref<string | null>(null)
const busy = ref(false)
const form = ref({ id: '', name: '', device: '', region: 'EU_868', preset: 'MEDIUM_FAST', tx_power_dbm: 22, relay_role: 'mute', copy_position: true })
const addError = ref('')
const preview = ref<Phy | null>(null)
const region = computed(() => props.regions.find((r) => r.name === form.value.region))
function openAdd() {
  const main = data.value?.radios[0]
  const taken = new Set([...(data.value?.radios ?? []).map((r) => r.phy.preset), ...(data.value?.pending ?? []).map((p) => p.preset)])
  const preset = (region.value?.presets ?? ['MEDIUM_FAST']).find((p) => !taken.has(p)) ?? 'MEDIUM_FAST'
  form.value = { id: '', name: '', device: '', region: main?.phy.region ?? 'EU_868', preset, tx_power_dbm: main?.phy.tx_power_dbm ?? 22, relay_role: 'mute', copy_position: true }
  addError.value = ''
  idTouched.value = false
  editingId.value = null
  suggest(preset)
  adding.value = true
}

function openEditPending(p: RadiosResponse['pending'][number]) {
  const main = data.value?.radios[0]
  form.value = {
    id: p.id, name: p.name || '', device: p.device || '', region: p.region || main?.phy.region || 'EU_868', preset: p.preset || 'LONG_FAST',
    tx_power_dbm: p.tx_power_dbm || main?.phy.tx_power_dbm || 22, relay_role: p.relay_role || 'mute', copy_position: true,
  }
  addError.value = ''
  idTouched.value = true // keep the id and name as they are
  editingId.value = p.id
  adding.value = true
}
// Suggest an id from the preset (mediumfast → mf) until the user types one.
const idTouched = ref(false)
function suggest(p: string) {
  if (idTouched.value || !p) return
  form.value.id = p.split('_').map((w) => w[0].toLowerCase()).join('')
  form.value.name = presetLabel(p)
}
watch(() => form.value.preset, suggest)
watch(
  () => [adding.value, form.value.region, form.value.preset, form.value.tx_power_dbm],
  async () => {
    if (!adding.value) return
    try {
      preview.value = await api.post<Phy>('/phy/preview', { region: form.value.region, preset: form.value.preset, tx_power_dbm: form.value.tx_power_dbm })
    } catch {
      preview.value = null
    }
  },
)
const sharesWith = computed(() => {
  if (!preview.value) return []
  const p = preview.value
  // Channels overlap when their centres are closer than half their bandwidths added together.
  return (data.value?.radios ?? []).filter((r) => Math.abs(r.phy.frequency_mhz - p.frequency_mhz) * 1000 < (r.phy.bw_khz + p.bw_khz) / 2).map((r) => r.name)
})
async function add() {
  addError.value = ''
  busy.value = true
  try {
    if (editingId.value) {
      await api.put(`/radios/${enc(editingId.value)}`, { name: form.value.name, driver: 'kiss', device: form.value.device, region: form.value.region,
        preset: form.value.preset, tx_power_dbm: form.value.tx_power_dbm, relay_role: form.value.relay_role })
      adding.value = false
      await load()
      toast(`${form.value.name || editingId.value} saved. It starts with these settings at the next restart.`)
      return
    }
    await api.post('/radios', { ...form.value, id: form.value.id.trim().toLowerCase() })
    adding.value = false
    idTouched.value = false
    emit('restart')
    await load()
    toast(`${form.value.name || form.value.id} added. Restart to bring it on air.`)
  } catch (e) {
    addError.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

const settingsFor = ref<string | null>(null)
function openSettings(id: string) {
  settingsFor.value = id
}
</script>

<template>
  <div class="max-w-4xl space-y-6">
    <div v-if="!data" class="h-40 animate-pulse rounded-xl bg-sunken" />
    <template v-else>
      <div class="flex flex-wrap items-end justify-between gap-3">
        <p class="max-w-2xl text-xs text-ink-3">
          Each radio is its own modem on its own preset, with its own relay persona and identities. Radios on the same channel take turns to transmit. <b>Edit</b> changes a radio's name and LoRa &amp; modem settings; the Relay, Airtime, Position and MQTT tabs follow the radio picked at the top of the page. Adding or removing a radio takes effect after a restart.
        </p>
        <button type="button" class="btn btn-primary" @click="openAdd"><Plus class="size-4" />Add radio</button>
      </div>

      <ul class="divide-y divide-line-soft rounded-xl border border-line-soft">
        <li v-for="r in data.radios" :key="r.id" class="flex flex-wrap items-center gap-3 px-3.5 py-3">
          <span class="flex size-9 shrink-0 items-center justify-center rounded-lg bg-sunken text-ink-2"><RadioIcon class="size-4" /></span>
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-2">
              <form v-if="renaming === r.id" class="flex items-center gap-2" @submit.prevent="saveName(r.id)">
                <label class="sr-only" :for="`rn-${r.id}`">Radio name</label>
                <input :id="`rn-${r.id}`" v-model="newName" class="input h-8 w-44" maxlength="40" autofocus @keydown.esc="renaming = null" />
                <button class="btn btn-sm btn-primary">Save</button>
                <button type="button" class="btn btn-sm" @click="renaming = null">Cancel</button>
              </form>
              <template v-else>
                <span class="text-[13px] font-medium">{{ r.name }}</span>
                <button type="button" class="icon-btn size-6" :aria-label="`Rename ${r.name}`" title="Rename" @click="startRename(r.id, r.name)"><Pencil class="size-3" /></button>
              </template>
              <span class="chip mono">{{ r.id }}</span>
              <span :class="['chip', r.connected ? 'bg-ok/14 text-ok' : 'bg-warn/15 text-warn']"><span :class="['dot size-1.5', r.connected ? 'bg-ok' : 'bg-warn']" />{{ r.connected ? 'on air' : 'modem not connected' }}</span>
              <span v-if="data.pending.some((p) => p.id === r.id && p.action === 'remove')" class="chip bg-bad/12 text-bad">removed · stops at restart</span>
            </div>
            <div class="mt-0.5 text-xs text-ink-3">
              {{ r.phy.preset_name }} · {{ r.phy.frequency_mhz.toFixed(3) }} MHz · {{ r.phy.tx_power_dbm }} dBm · relay {{ r.relay.role }} · {{ r.identities }} {{ r.identities === 1 ? 'identity' : 'identities' }}
              <span class="mono"> · {{ r.device || r.driver }}</span>
              <span v-if="r.overlaps?.length" class="text-warn"> · shares its channel with {{ r.overlaps.join(', ') }}</span>
            </div>
          </div>
          <button type="button" class="btn btn-sm" title="Name and LoRa & modem settings" @click="openSettings(r.id)">
            <Settings2 class="size-3.5" />Edit
          </button>
          <button v-if="!r.main" type="button" class="icon-btn" :aria-label="`Remove ${r.name}`" title="Remove" @click="remove(r.id, r.name, true)"><Trash class="size-4" /></button>
        </li>
        <li v-for="p in data.pending.filter((x) => x.action === 'start')" :key="p.id" class="flex flex-wrap items-center gap-3 bg-raised/50 px-3.5 py-3">
          <span class="flex size-9 shrink-0 items-center justify-center rounded-lg border border-dashed border-line text-ink-3"><RadioIcon class="size-4" /></span>
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-2">
              <span class="text-[13px] font-medium">{{ p.name || p.id }}</span>
              <span class="chip mono">{{ p.id }}</span>
              <span class="chip bg-warn/15 text-warn">starts at restart</span>
            </div>
            <div class="mt-0.5 text-xs text-ink-3">{{ presetLabel(p.preset || 'LONG_FAST') }} · relay {{ p.relay_role }} <span class="mono"> · {{ p.device || p.driver }}</span></div>
          </div>
          <button type="button" class="btn btn-sm" title="Change this radio before it starts" @click="openEditPending(p)"><Settings2 class="size-3.5" />Edit</button>
          <button type="button" class="icon-btn" :aria-label="`Remove ${p.name || p.id}`" title="Remove" @click="remove(p.id, p.name || p.id, false)"><Trash class="size-4" /></button>
        </li>
      </ul>

      <form class="grid gap-3 rounded-xl border border-line-soft p-4 sm:grid-cols-[1fr_auto]" @submit.prevent="saveSite">
        <div>
          <label class="label" for="site-duty">Site airtime cap · {{ siteDuty ? `${siteDuty}% of the last hour, all radios together` : 'off' }}</label>
          <input id="site-duty" v-model.number="siteDuty" type="range" min="0" max="36" step="1" class="w-full accent-[var(--brand)]" />
          <p class="hint">
            On top of each radio's own duty limit. Radios still take turns on a shared channel with this off.
            <template v-if="site && site.coordinator && site.running_duty_cycle_percent !== site.duty_cycle_percent"> Running with {{ num(site.running_duty_cycle_percent) }}%.</template>
          </p>
        </div>
        <div class="flex items-end">
          <button class="btn" :disabled="savingSite || !site || siteDuty === site.duty_cycle_percent"><Spinner v-if="savingSite" />Save cap</button>
        </div>
      </form>
    </template>

    <Modal :open="adding" :title="editingId ? `Edit ${form.name || editingId}` : 'Add a radio'" :subtitle="editingId ? 'Not started yet: these settings apply when it starts at the next restart.' : 'Another Mesh KISS modem on this host, on its own preset.'" @close="adding = false">
      <form class="grid gap-4 sm:grid-cols-2" @submit.prevent="add">
        <div>
          <label class="label" for="ar-region">Region</label>
          <select id="ar-region" v-model="form.region" class="input">
            <option v-for="r in regions" :key="r.name" :value="r.name">{{ r.name }}</option>
          </select>
        </div>
        <div>
          <label class="label" for="ar-preset">Preset</label>
          <select id="ar-preset" v-model="form.preset" class="input">
            <option v-for="p in region?.presets ?? [form.preset]" :key="p" :value="p">{{ presetLabel(p) }}</option>
          </select>
        </div>
        <div>
          <label class="label" for="ar-id">ID</label>
          <input id="ar-id" v-model="form.id" class="input mono" placeholder="mf" maxlength="24" pattern="[a-z0-9-]{1,24}" required :readonly="!!editingId" @input="idTouched = true" />
          <p class="hint">Lowercase letters, digits, dashes. Names its state folder; can't be changed later.</p>
        </div>
        <div>
          <label class="label" for="ar-name">Name</label>
          <input id="ar-name" v-model="form.name" class="input" placeholder="MediumFast" maxlength="40" />
        </div>
        <div class="sm:col-span-2">
          <label class="label" for="ar-dev">Serial device</label>
          <input id="ar-dev" v-model="form.device" class="input mono" list="ar-ports" placeholder="/dev/serial/by-id/…" required />
          <datalist id="ar-ports"><option v-for="p in ports.filter((x) => !usedDevices.has(x.path))" :key="p.path" :value="p.path">{{ p.description }}</option></datalist>
          <p class="hint">Use a <span class="mono">/dev/serial/by-id/</span> or udev name so it survives replugging. Flash the Mesh KISS firmware on it first.</p>
        </div>
        <div>
          <label class="label" for="ar-pwr">TX power · {{ form.tx_power_dbm }} dBm</label>
          <input id="ar-pwr" v-model.number="form.tx_power_dbm" type="range" min="1" :max="region?.power_limit_dbm || 27" step="1" class="w-full accent-[var(--brand)]" />
        </div>
        <div>
          <label class="label" for="ar-role">Relay persona</label>
          <select id="ar-role" v-model="form.relay_role" class="input">
            <option v-for="(label, role) in roleLabel" :key="role" :value="role">{{ label }}</option>
          </select>
          <p class="hint">Starts muted, so a new radio doesn't repeat until you decide it should.</p>
        </div>
        <label v-if="!editingId" class="flex items-center gap-2 text-[13px] sm:col-span-2"><input v-model="form.copy_position" type="checkbox" class="size-4 accent-[var(--brand)]" /> Same site position as the main radio</label>
        <p v-if="preview" class="hint sm:col-span-2">
          {{ preview.frequency_mhz.toFixed(3) }} MHz · {{ num(preview.bw_khz) }} kHz · SF{{ preview.sf }}
          <span v-if="sharesWith.length" class="!text-warn"><TriangleAlert class="mx-1 inline size-3.5 align-[-2px]" />Same channel as {{ sharesWith.join(', ') }}: they'll take turns to transmit.</span>
        </p>
        <p v-if="addError" class="hint !text-bad sm:col-span-2">{{ addError }}</p>
        <div class="flex justify-end gap-2 sm:col-span-2">
          <button type="button" class="btn" @click="adding = false">Cancel</button>
          <button class="btn btn-primary" :disabled="busy || !form.id || !form.device"><Spinner v-if="busy" />{{ editingId ? 'Save radio' : 'Add radio' }}</button>
        </div>
      </form>
    </Modal>
    <RadioSettingsModal :radio-id="settingsFor" :ports="ports" :regions="regions" @close="settingsFor = null; load()" />
  </div>
</template>
