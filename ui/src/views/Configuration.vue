<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Download, KeyRound, Plus, RotateCcw, Trash, Upload } from '@lucide/vue'
import { api, API_BASE, enc, MAIN_RADIO, setToken as setAuthToken, token as authToken, withRadio } from '@/api/client'
import type { ApiToken, Config, ConfigPutResult, Region, SerialPort } from '@/api/types'
import { live, refreshStatus } from '@/store/live'
import Modal from '@/components/ui/Modal.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import Spinner from '@/components/ui/Spinner.vue'
import MqttConnections from '@/components/config/MqttConnections.vue'
import RadiosPanel from '@/components/config/RadiosPanel.vue'
import MeshtasticdPanel from '@/components/config/MeshtasticdPanel.vue'
import RelayFavorites from '@/components/config/RelayFavorites.vue'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'
import { now } from '@/composables/now'
import { num, relTime } from '@/lib/format'
import { rebroadcastModes, relayModes } from '@/lib/relay'

// Leaflet stays in its own chunk until the Position tab opens.
const PositionPicker = defineAsyncComponent({ loader: () => import('@/components/nodes/PositionPicker.vue'), loadingComponent: Spinner })

type Tab = 'radio' | 'relay' | 'airtime' | 'position' | 'mqtt' | 'web' | 'meshtasticd' | 'backup'
const allTabs: { id: Tab; label: string }[] = [
  { id: 'radio', label: 'Radios' },
  { id: 'relay', label: 'Relay' },
  { id: 'airtime', label: 'Airtime & duty' },
  { id: 'position', label: 'Position & hardware' },
  { id: 'mqtt', label: 'MQTT' },
  { id: 'web', label: 'Web & API tokens' },
  { id: 'meshtasticd', label: 'meshtasticd' },
  { id: 'backup', label: 'Backup & restore' },
]
// The Radios list, web settings, tokens and backups are site-wide: always shown, whichever radio
// the tabs below are pointed at.
const tabs = allTabs
// Tabs whose form edits one radio's settings (GET/PUT /config for that radio).
const radioTabs: Tab[] = ['relay', 'airtime', 'position', 'mqtt']
const route = useRoute()
const router = useRouter()
const oldTabs: Record<string, Tab> = { experimental: 'meshtasticd' } // old links
const tab = computed<Tab>(() => {
  const want = oldTabs[route.params.tab as string] ?? route.params.tab
  return tabs.some((t) => t.id === want) ? (want as Tab) : 'radio' // 'radios' (old link) → Radios
})
const setTab = (t: Tab) => router.replace({ name: 'config', params: { tab: t } })

const severalRadios = computed(() => live.radios.length > 1)
// Which radio the Relay / Airtime & duty / Position & hardware / MQTT tabs are showing and edit.
const configRadio = ref(MAIN_RADIO)
const saved = ref<Config | null>(null)
const form = ref<Config | null>(null)
const saving = ref(false)

async function load() {
  const c = await api.get<Config>(withRadio('/config', configRadio.value))
  saved.value = c
  form.value = structuredClone(c)
}

/** Switches which radio the form edits, asking first if there are unsaved changes on this tab. */
async function switchRadio(id: string) {
  if (id === configRadio.value) return
  if (dirty.value) {
    const ok = await confirmDialog({
      title: 'Switch radios?',
      body: `Unsaved changes on this tab will be lost.`,
      confirm: 'Discard and switch',
      danger: true,
    })
    if (!ok) return
  }
  configRadio.value = id
  try {
    await load()
  } catch (e) {
    toastError(e)
  }
}

// The selector wants the main radio's real id, not the "main" sentinel, so it can highlight it;
// live.radios loads asynchronously, so pick it up once it arrives (no reload: same radio either way).
watch(
  () => live.radios,
  (radios) => {
    if (configRadio.value !== MAIN_RADIO) return
    const main = radios.find((r) => r.main)
    if (main) configRadio.value = main.id
  },
  { immediate: true },
)

// Web settings belong to the main radio: that tab always shows (and saves) the main radio's.
watch(tab, (t) => {
  const main = live.radios.find((r) => r.main)?.id ?? MAIN_RADIO
  if (t === 'web' && configRadio.value !== main) {
    configRadio.value = main
    load().catch(toastError)
  }
})

const section = computed(() => (['backup', 'meshtasticd', 'radio'].includes(tab.value) ? null : tab.value === 'position' ? 'position' : tab.value))
// The Position tab edits two config sections.
const sections = computed<(keyof Config)[]>(() => (tab.value === 'position' ? ['position', 'hardware'] : section.value ? [section.value as keyof Config] : []))
const dirty = computed(() => {
  if (!form.value || !saved.value) return false
  return sections.value.some((s) => JSON.stringify(form.value![s]) !== JSON.stringify(saved.value![s]))
})

async function save() {
  if (!sections.value.length || !form.value) return
  saving.value = true
  try {
    const body: Record<string, unknown> = {}
    for (const s of sections.value) body[s] = form.value[s]
    const r = await api.put<ConfigPutResult>(withRadio('/config', configRadio.value), body)
    saved.value = r.config
    const next = { ...form.value }
    for (const s of sections.value) (next as Record<string, unknown>)[s] = structuredClone(r.config[s])
    form.value = next
    toast(r.restart_required ? 'Saved. Restart the daemon to apply.' : 'Saved and applied')
    refreshStatus().catch(() => {})
  } catch (e) {
    toastError(e)
  } finally {
    saving.value = false
  }
}
function revert() {
  if (!form.value || !saved.value) return
  const next = { ...form.value }
  for (const s of sections.value) (next as Record<string, unknown>)[s] = structuredClone(saved.value[s])
  form.value = next
}

async function confirmDutyOverride() {
  if (!form.value?.airtime.override_duty_cycle) return
  const ok = await confirmDialog({
    title: 'Ignore the duty-cycle limit?',
    body: `This radio will transmit without the hourly airtime budget. ${form.value.radio.region} sets a legal duty-cycle limit, and a busy relay can exceed it. Only turn this on if you know your site and licence allow it.`,
    confirm: 'Ignore the limit',
    danger: true,
  })
  if (!ok && form.value) form.value.airtime.override_duty_cycle = false
}

const hwModels = ['AUTO', 'HELTEC_V3', 'HELTEC_V4', 'HELTEC_WIRELESS_TRACKER', 'RAK4631', 'SEEED_XIAO_S3', 'XIAO_NRF52_KIT', 'TBEAM', 'T_ECHO', 'PORTDUINO']

// ---- radio
const ports = ref<SerialPort[]>([])
const regions = ref<Region[]>([])
const region = computed(() => regions.value.find((r) => r.name === form.value?.radio.region))

// ---- web: password + tokens
const pw = ref({ current: '', next: '', repeat: '' })
const pwBusy = ref(false)
async function changePassword() {
  pwBusy.value = true
  try {
    const r = await api.put<{ token: string }>('/auth/password', { current: pw.value.current, new: pw.value.next })
    if (r?.token) setAuthToken(r.token)
    pw.value = { current: '', next: '', repeat: '' }
    toast('Password changed. Other browsers have been signed out.')
  } catch (e) {
    toastError(e)
  } finally {
    pwBusy.value = false
  }
}

const tokens = ref<ApiToken[]>([])
const newTokenName = ref('')
const created = ref<ApiToken | null>(null)
async function loadTokens() {
  tokens.value = await api.get<ApiToken[]>('/tokens')
}
async function createToken() {
  const name = newTokenName.value.trim()
  if (!name) return
  try {
    created.value = await api.post<ApiToken>('/tokens', { name })
    newTokenName.value = ''
    loadTokens()
  } catch (e) {
    toastError(e)
  }
}
async function revoke(t: ApiToken) {
  if (!(await confirmDialog({ title: `Revoke “${t.name}”?`, body: 'Anything using this token (Home Assistant, scripts, Prometheus) will get 401 errors straight away.', confirm: 'Revoke token', danger: true }))) return
  try {
    await api.del(`/tokens/${enc(t.id)}`)
    tokens.value = tokens.value.filter((x) => x.id !== t.id)
    toast('Token revoked')
  } catch (e) {
    toastError(e)
  }
}

// ---- backup
const downloading = ref(false)
async function download() {
  downloading.value = true
  try {
    const res = await fetch(`${API_BASE}/backup`, { headers: { Authorization: `Bearer ${authToken.value}` } })
    if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error ?? `${res.status} ${res.statusText}`)
    const blob = await res.blob()
    const name = /filename="?([^"]+)"?/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? `repeatertastic-backup-${new Date().toISOString().slice(0, 10)}.json`
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = name
    a.click()
    setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  } catch (e) {
    toastError(e)
  } finally {
    downloading.value = false
  }
}
const restoreFile = ref<File | null>(null)
const restoring = ref(false)
async function restore() {
  const f = restoreFile.value
  if (!f) return
  let body: unknown
  try {
    body = JSON.parse(await f.text())
  } catch {
    toastError(new Error('That file is not valid JSON'))
    return
  }
  if (!(await confirmDialog({ title: 'Restore this backup?', body: `The config and identity keys from ${f.name} replace what's on this host when the daemon next restarts. Download a backup of the current state first if you might need it.`, confirm: 'Restore', danger: true }))) return
  restoring.value = true
  try {
    const r = await api.post<{ restart_required: boolean }>('/restore', body)
    toast(r.restart_required ? 'Backup restored. Restart the daemon to load its identities.' : 'Backup restored')
    load()
  } catch (e) {
    toastError(e)
  } finally {
    restoring.value = false
  }
}

onMounted(async () => {
  try {
    await load()
  } catch (e) {
    toastError(e)
  }
  api.get<SerialPort[]>('/serial-ports').then((p) => (ports.value = p), () => {})
  api.get<Region[]>('/regions').then((r) => (regions.value = r), () => {})
  loadTokens().catch(() => {})
})

// Loads one tile around the site from the saved (key-filled) URL.
const tileTest = ref('')
const tileError = ref(false)
function testTile() {
  const url = live.status?.map?.tile_url
  if (!url) return
  const lat = form.value?.position.latitude || 55.95
  const lon = form.value?.position.longitude || -3.19
  const z = 10
  const x = Math.floor(((lon + 180) / 360) * 2 ** z)
  const r = (lat * Math.PI) / 180
  const y = Math.floor(((1 - Math.log(Math.tan(r) + 1 / Math.cos(r)) / Math.PI) / 2) * 2 ** z)
  tileError.value = false
  tileTest.value = url.replace('{s}', 'a').replace('{z}', String(z)).replace('{x}', String(x)).replace('{y}', String(y)).replace('{r}', '')
}

const tokenExample = computed(() => `curl -H "Authorization: Bearer $TOKEN" ${location.origin}${API_BASE}/status`)
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Configuration</h2>
        <p class="page-sub">Changes apply live where it is safe; otherwise you'll be asked to restart.</p>
      </div>
    </div>


    <section class="card overflow-hidden">
      <div class="tabs px-3 sm:px-4" role="tablist">
        <button type="button" v-for="t in tabs" :key="t.id" role="tab" :aria-selected="tab === t.id" @click="setTab(t.id)">{{ t.label }}</button>
      </div>

      <div v-if="!form" class="p-6"><div class="h-48 animate-pulse rounded-xl bg-sunken" /></div>

      <div v-else class="p-4 sm:p-6">
        <!-- Relay / Airtime / Position / MQTT edit one radio's settings: pick which on a multi-radio site. -->
        <div v-if="severalRadios && radioTabs.includes(tab)" class="mb-4 flex items-center gap-2">
          <label class="label !mb-0" for="cfg-radio">Settings for</label>
          <select id="cfg-radio" class="input !h-8 !w-auto !py-0 text-[13px]" :value="configRadio" @change="switchRadio(($event.target as HTMLSelectElement).value)">
            <option v-for="r in live.radios" :key="r.id" :value="r.id">{{ r.name }}</option>
          </select>
        </div>

        <!-- RADIOS: the site's radios; each is edited in a modal -->
        <RadiosPanel v-if="tab === 'radio'" :ports="ports" :regions="regions" @restart="refreshStatus()" />

        <!-- RELAY -->
        <div v-else-if="tab === 'relay'" class="grid max-w-2xl gap-4 sm:grid-cols-[1fr_8rem]">
          <div class="sm:col-span-2">
            <span class="label">Role</span>
            <div class="seg">
              <button type="button" v-for="r in relayModes" :key="r.id" :title="r.title" :aria-pressed="form.relay.role === r.id" @click="form.relay.role = r.id">{{ r.label }}</button>
            </div>
            <p class="hint">Meshtastic device roles, the same switch as in the top bar, applied to the relay's meshtasticd. Router always repeats and router late does so last; client repeats after routers and client base also favours its favourites; client mute never repeats. Monitor only listens (nothing is transmitted); off ignores the radio.</p>
          </div>
          <RelayFavorites v-if="form.relay.role === 'client_base'" v-model="form.relay.favorites" class="sm:col-span-2" />
          <div class="sm:col-span-2">
            <label class="label" for="c-rebroadcast">Rebroadcast mode</label>
            <select id="c-rebroadcast" v-model="form.relay.rebroadcast" class="input">
              <option v-for="m in rebroadcastModes" :key="m.id" :value="m.id" :title="m.title">{{ m.label }}</option>
            </select>
            <p class="hint">{{ rebroadcastModes.find((m) => m.id === form?.relay.rebroadcast)?.title }}. Meshtastic's device.rebroadcast_mode on the relay.</p>
          </div>
          <div>
            <label class="label" for="c-rln">Relay long name</label>
            <input id="c-rln" v-model="form.relay.long_name" class="input" maxlength="39" />
          </div>
          <div>
            <label class="label" for="c-rsn">Short name</label>
            <input id="c-rsn" v-model="form.relay.short_name" class="input mono uppercase" maxlength="4" />
          </div>
          <div class="sm:col-span-2">
            <label class="label" for="c-ldm">Local DMs between identities</label>
            <select id="c-ldm" v-model="form.relay.local_dm" class="input sm:max-w-sm">
              <option value="software">Deliver in software (no RF, synthesised ACK)</option>
              <option value="also_rf">Also transmit on RF (visible on the mesh)</option>
            </select>
          </div>
        </div>

        <!-- AIRTIME -->
        <div v-else-if="tab === 'airtime'" class="grid max-w-3xl gap-4 sm:grid-cols-2">
          <div>
            <label class="label" for="c-duty">Duty cycle limit · {{ num(form.airtime.duty_cycle_percent) }}%</label>
            <input id="c-duty" v-model.number="form.airtime.duty_cycle_percent" type="range" min="1" :max="Math.max(region?.duty_cycle_pct ?? 100, 1)" step="0.5" class="w-full accent-[var(--brand)]" />
            <p class="hint">Rolling one-hour TX budget shared by all identities. {{ form.radio.region }} allows {{ region?.duty_cycle_pct ?? '…' }}%.</p>
          </div>
          <div>
            <label class="label" for="c-share">Default identity share · {{ form.airtime.identity_share_percent }}%</label>
            <input id="c-share" v-model.number="form.airtime.identity_share_percent" type="range" min="5" max="100" step="5" class="w-full accent-[var(--brand)]" />
            <p class="hint">Slice of that budget one identity may use before it is flagged “Over share”.</p>
          </div>
          <div>
            <label class="label" for="c-ni">NodeInfo broadcast interval</label>
            <select id="c-ni" v-model="form.airtime.nodeinfo_interval" class="input">
              <option v-for="v in ['1h', '3h', '6h', '12h', '24h']" :key="v" :value="v">every {{ v }}</option>
            </select>
            <p class="hint">Staggered across identities so they don't all transmit at once.</p>
          </div>
          <div>
            <label class="label" for="c-tel">Relay device telemetry</label>
            <select id="c-tel" v-model="form.airtime.telemetry_interval" class="input">
              <option value="off">Off</option>
              <option v-for="v in ['30m', '1h', '3h', '6h', '12h']" :key="v" :value="v">every {{ v }}</option>
            </select>
            <p class="hint">The relay persona reports uptime, channel use and TX airtime like a mains-powered node. Skipped when the channel is busy.</p>
          </div>
          <div class="sm:col-span-2 rounded-xl border border-line-soft px-3.5 py-3">
            <label class="flex items-start gap-2 text-[13px]">
              <input v-model="form.airtime.override_duty_cycle" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" @change="confirmDutyOverride" />
              <span :class="form.airtime.override_duty_cycle ? 'text-bad' : ''">Ignore the duty-cycle limit<span class="block text-xs text-ink-3">Transmits without an hourly budget. {{ form.radio.region }} limits transmit time by law ({{ region?.duty_cycle_pct ?? '…' }}%); you're responsible for staying legal.</span></span>
            </label>
          </div>
          <p class="hint sm:col-span-2">Contention window: CW {{ form.airtime.cw_min }}–{{ form.airtime.cw_max }}, fixed to match the firmware so this site waits its turn like every other node. Position broadcasts are set on Position &amp; hardware.</p>
        </div>

        <!-- POSITION & HARDWARE -->
        <div v-else-if="tab === 'position'" class="grid max-w-3xl gap-4 sm:grid-cols-2">
          <div class="sm:col-span-2">
            <div class="flex items-end justify-between gap-2">
              <h4 class="eyebrow mb-1">Site position</h4>
              <button v-if="form.position.latitude || form.position.longitude" type="button" class="btn btn-sm btn-ghost" @click="form.position.latitude = 0; form.position.longitude = 0">Clear</button>
            </div>
            <p class="hint !mt-0">A fixed location broadcast like a fixed node and answered on request. Drop a pin on the map (click, or drag the pin) or type it in. Both zero means no position.</p>
            <div class="mt-2 h-72 overflow-hidden rounded-xl border border-line-soft">
              <PositionPicker v-model:latitude="form.position.latitude" v-model:longitude="form.position.longitude" />
            </div>
          </div>
          <div>
            <label class="label" for="p-lat">Latitude</label>
            <input id="p-lat" v-model.number="form.position.latitude" type="number" step="0.000001" min="-90" max="90" class="input tabular-nums" />
          </div>
          <div>
            <label class="label" for="p-lon">Longitude</label>
            <input id="p-lon" v-model.number="form.position.longitude" type="number" step="0.000001" min="-180" max="180" class="input tabular-nums" />
          </div>
          <div>
            <label class="label" for="p-alt">Altitude (m above sea level)</label>
            <input id="p-alt" v-model.number="form.position.altitude" type="number" class="input tabular-nums" />
          </div>
          <div>
            <label class="label" for="p-bits">Precision · {{ form.position.precision_bits === 32 ? 'exact' : `${form.position.precision_bits} bits` }}</label>
            <input id="p-bits" v-model.number="form.position.precision_bits" type="range" min="10" max="32" step="1" class="w-full accent-[var(--brand)]" />
            <p class="hint">32 is exact; 16 ≈ 360 m, 13 ≈ 3 km.</p>
          </div>
          <div>
            <label class="label" for="p-int">Broadcast interval</label>
            <select id="p-int" v-model="form.position.interval" class="input">
              <option v-for="v in ['30m', '1h', '3h', '6h', '12h', '24h']" :key="v" :value="v">every {{ v }}</option>
            </select>
          </div>
          <div>
            <label class="label" for="p-who">Broadcast from</label>
            <select id="p-who" v-model="form.position.identities" class="input">
              <option value="relay">The relay persona only</option>
              <option value="all">Every identity</option>
            </select>
          </div>
          <div class="sm:col-span-2">
            <h4 class="eyebrow mb-1 mt-2">Hardware</h4>
          </div>
          <div>
            <label class="label" for="p-hw">Advertised hardware model</label>
            <select id="p-hw" v-model="form.hardware.hw_model" class="input">
              <option v-for="m in hwModels" :key="m" :value="m">{{ m === 'AUTO' ? `Auto (the modem's board)` : m }}</option>
            </select>
            <p class="hint">
              Now advertising <span class="mono">{{ saved?.hardware.effective }}</span><template v-if="saved?.hardware.modem"> · modem reports “{{ saved.hardware.modem }}”</template>.
            </p>
          </div>
        </div>

        <!-- MQTT -->
        <MqttConnections v-else-if="tab === 'mqtt'" v-model="form.mqtt" />

        <!-- MESHTASTICD -->
        <MeshtasticdPanel v-else-if="tab === 'meshtasticd'" class="max-w-3xl" />

        <!-- WEB -->
        <div v-else-if="tab === 'web'" class="grid gap-8 xl:grid-cols-2">
          <div class="space-y-6">
            <div class="grid gap-4 sm:grid-cols-[1fr_7rem]">
              <div>
                <label class="label" for="c-bind">Bind address</label>
                <input id="c-bind" v-model="form.web.bind" class="input mono" />
              </div>
              <div>
                <label class="label" for="c-wport">Port</label>
                <input id="c-wport" v-model.number="form.web.port" type="number" class="input tabular-nums" />
              </div>
              <div>
                <label class="label" for="c-ttl">Browser session length</label>
                <select id="c-ttl" v-model="form.web.session_ttl" class="input">
                  <option v-for="v in ['1h', '12h', '24h', '168h', '720h']" :key="v" :value="v">{{ v }}</option>
                </select>
              </div>
              <div>
                <label class="label" for="c-log">Log level</label>
                <select id="c-log" v-model="form.web.log_level" class="input">
                  <option v-for="v in ['debug', 'info', 'warn', 'error']" :key="v" :value="v">{{ v }}</option>
                </select>
              </div>
              <label class="flex items-start gap-2 text-[13px] sm:col-span-2">
                <input v-model="form.web.mdns" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" />
                <span>Advertise identities on the LAN (mDNS)<span class="block text-xs text-ink-3">Lets the Meshtastic apps discover each identity's port. Needs a restart.</span></span>
              </label>
            </div>
            <div class="rounded-xl border border-line-soft p-4">
              <h4 class="card-title mb-3">Map tiles</h4>
              <label class="label" for="c-tiles">Tile URL</label>
              <input id="c-tiles" v-model.trim="form.web.map_tile_url" class="input mono" placeholder="default: CARTO Positron" />
              <p class="hint">
                Leaflet template with <span class="mono">{z}</span>, <span class="mono">{x}</span>, <span class="mono">{y}</span>; <span class="mono">{s}</span> and <span class="mono">{r}</span> are optional and
                <span class="mono">{api_key}</span> is filled from the {{ form.web.map_key_source === 'none' ? 'environment (none set)' : `${form.web.map_key_source} key` }}. Empty uses the default.
              </p>
              <div class="mt-3 flex flex-wrap items-center gap-3">
                <button type="button" class="btn btn-sm" @click="testTile">Test saved tiles</button>
                <img v-if="tileTest" :src="tileTest" alt="Test map tile near the site" width="96" height="96" class="rounded-lg border border-line-soft" @error="tileError = true" @load="tileError = false" />
                <span v-if="tileTest && tileError" class="text-xs text-bad">That tile didn't load. Check the URL and key.</span>
              </div>
            </div>
            <form class="rounded-xl border border-line-soft p-4" @submit.prevent="changePassword">
              <h4 class="card-title mb-3">Change admin password</h4>
              <div class="grid gap-3 sm:grid-cols-3">
                <input id="pw-current" v-model="pw.current" aria-label="Current password" type="password" class="input" placeholder="Current" autocomplete="current-password" />
                <input id="pw-next" v-model="pw.next" aria-label="New password" type="password" class="input" placeholder="New (8+ chars)" autocomplete="new-password" />
                <input id="pw-repeat" v-model="pw.repeat" aria-label="Repeat new password" type="password" class="input" placeholder="Repeat new" autocomplete="new-password" />
              </div>
              <div class="mt-3 flex items-center justify-between gap-2">
                <span v-if="pw.repeat && pw.next !== pw.repeat" class="text-xs text-bad">Passwords don't match</span><span v-else />
                <button type="submit" class="btn btn-sm" :disabled="pwBusy || !pw.current || pw.next.length < 8 || pw.next !== pw.repeat"><Spinner v-if="pwBusy" />Change password</button>
              </div>
            </form>
          </div>

          <div>
            <h4 class="card-title">API tokens</h4>
            <p class="mb-3 mt-0.5 text-xs text-ink-3">For Home Assistant, scripts and Prometheus. A token is shown once when created.</p>
            <form class="mb-3 flex gap-2" @submit.prevent="createToken">
              <input id="token-name" v-model="newTokenName" aria-label="Token name" class="input" placeholder="Token name, e.g. Home Assistant" maxlength="40" />
              <button type="submit" class="btn btn-primary shrink-0" :disabled="!newTokenName.trim()"><Plus class="size-4" />Create</button>
            </form>
            <div class="overflow-hidden rounded-xl border border-line-soft">
              <div v-for="t in tokens" :key="t.id" class="flex items-center gap-3 border-b border-line-soft px-3.5 py-2.5 last:border-b-0">
                <KeyRound class="size-4 shrink-0 text-ink-3" />
                <div class="min-w-0 flex-1">
                  <div class="truncate text-[13px] font-medium">{{ t.name }}</div>
                  <div class="text-2xs text-ink-3">created {{ relTime(t.created_at, now) }} · {{ t.last_used ? `last used ${relTime(t.last_used, now)}` : 'never used' }}</div>
                </div>
                <button type="button" class="icon-btn hover:!text-bad" title="Revoke" @click="revoke(t)"><Trash class="size-4" /></button>
              </div>
              <div v-if="!tokens.length" class="px-4 py-6 text-center text-xs text-ink-3">No API tokens yet.</div>
            </div>
          </div>
        </div>

        <!-- BACKUP -->
        <div v-else class="grid gap-4 lg:grid-cols-2">
          <div class="rounded-xl border border-line-soft p-5">
            <Download class="size-5 text-brand" />
            <h4 class="card-title mt-2">Download backup</h4>
            <p class="mt-1 text-[13px] text-ink-3">One JSON file with the config (including MQTT passwords) and every identity's private key. Store it like a password.</p>
            <button type="button" class="btn mt-4" :disabled="downloading" @click="download"><Spinner v-if="downloading" /><Download v-else class="size-4" />Download backup</button>
          </div>
          <div class="rounded-xl border border-line-soft p-5">
            <Upload class="size-5 text-warn" />
            <h4 class="card-title mt-2">Restore</h4>
            <p class="mt-1 text-[13px] text-ink-3">Replaces the config and identities on this host with those from a backup file.</p>
            <div class="mt-4 flex flex-wrap items-center gap-2">
              <label class="btn cursor-pointer">
                <Upload class="size-4" />{{ restoreFile ? restoreFile.name : 'Choose file…' }}
                <input type="file" accept="application/json,.json" class="sr-only" @change="restoreFile = ($event.target as HTMLInputElement).files?.[0] ?? null" />
              </label>
              <button type="button" class="btn btn-danger" :disabled="!restoreFile || restoring" @click="restore"><Spinner v-if="restoring" />Restore</button>
            </div>
          </div>
        </div>
      </div>

      <div v-if="form && section" class="flex items-center justify-end gap-2 border-t border-line-soft bg-raised/60 px-4 py-3 sm:px-6">
        <span v-if="dirty" class="mr-auto text-xs text-warn">Unsaved changes</span>
        <button type="button" class="btn btn-sm" :disabled="!dirty" @click="revert"><RotateCcw class="size-3.5" />Revert</button>
        <button type="button" class="btn btn-sm btn-primary" :disabled="!dirty || saving" @click="save"><Spinner v-if="saving" />Save changes</button>
      </div>
    </section>

    <Modal :open="!!created" title="API token created" size="md" @close="created = null">
      <p class="text-[13px] text-ink-2">Copy <b>{{ created?.name }}</b> now. It won't be shown again.</p>
      <div class="mt-3 flex items-center gap-1 rounded-xl border border-brand/40 bg-brand/6 px-3 py-2.5">
        <span class="mono min-w-0 flex-1 break-all">{{ created?.token }}</span>
        <CopyButton :text="created?.token ?? ''" label="Token" />
      </div>
      <p class="mt-3 text-xs text-ink-3">Example</p>
      <pre class="mono scroll-thin mt-1 overflow-x-auto rounded-lg bg-sunken px-3 py-2 text-[11.5px]">{{ tokenExample }}</pre>
      <template #footer><button type="button" class="btn btn-primary" @click="created = null">I've copied it</button></template>
    </Modal>
  </div>
</template>
