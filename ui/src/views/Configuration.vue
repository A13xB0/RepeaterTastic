<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Download, KeyRound, Plus, RotateCcw, TriangleAlert, Trash, Upload } from '@lucide/vue'
import { api, API_BASE, enc, setToken as setAuthToken, token as authToken } from '@/api/client'
import type { ApiToken, Config, ConfigPutResult, Phy, Region, SerialPort } from '@/api/types'
import { live, refreshStatus } from '@/store/live'
import Modal from '@/components/ui/Modal.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'
import { now } from '@/composables/now'
import { relTime } from '@/lib/format'

type Tab = 'radio' | 'relay' | 'airtime' | 'position' | 'mqtt' | 'web' | 'backup'
const allTabs: { id: Tab; label: string }[] = [
  { id: 'radio', label: 'Radio' },
  { id: 'relay', label: 'Relay' },
  { id: 'airtime', label: 'Airtime & duty' },
  { id: 'position', label: 'Position & hardware' },
  { id: 'mqtt', label: 'MQTT' },
  { id: 'web', label: 'Web & API tokens' },
  { id: 'backup', label: 'Backup & restore' },
]
// Web settings, tokens and backups belong to the host, so they only show on the main radio.
const tabs = computed(() => allTabs.filter((t) => saved.value?.main !== false || (t.id !== 'web' && t.id !== 'backup')))
const route = useRoute()
const router = useRouter()
const tab = computed<Tab>(() => (tabs.value.some((t) => t.id === route.params.tab) ? (route.params.tab as Tab) : 'radio'))
const setTab = (t: Tab) => router.replace({ name: 'config', params: { tab: t } })

const saved = ref<Config | null>(null)
const form = ref<Config | null>(null)
const saving = ref(false)
const restartRequired = ref(false)

async function load() {
  const c = await api.get<Config>('/config')
  saved.value = c
  form.value = structuredClone(c)
}

const section = computed(() => (tab.value === 'backup' ? null : tab.value === 'position' ? 'position' : tab.value))
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
    const r = await api.put<ConfigPutResult>('/config', body)
    saved.value = r.config
    const next = { ...form.value }
    for (const s of sections.value) (next as Record<string, unknown>)[s] = structuredClone(r.config[s])
    form.value = next
    restartRequired.value ||= r.restart_required
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

const restarting = ref(false)
async function restartNow() {
  if (!(await confirmDialog({ title: 'Restart RepeaterTastic?', body: 'Every identity drops off air and its app connections close for about 15 seconds while the daemon restarts with the saved settings.', confirm: 'Restart' }))) return
  restarting.value = true
  try {
    await api.post('/restart')
    toast('Restarting… the page reconnects by itself')
    restartRequired.value = false
  } catch (e) {
    toastError(e)
  } finally {
    setTimeout(() => (restarting.value = false), 15000)
  }
}

const hwModels = ['AUTO', 'HELTEC_V3', 'HELTEC_V4', 'HELTEC_WIRELESS_TRACKER', 'RAK4631', 'SEEED_XIAO_S3', 'XIAO_NRF52_KIT', 'TBEAM', 'T_ECHO', 'PORTDUINO']

// ---- radio
const ports = ref<SerialPort[]>([])
const regions = ref<Region[]>([])
const phy = ref<Phy | null>(null)
const region = computed(() => regions.value.find((r) => r.name === form.value?.radio.region))
async function preview() {
  if (!form.value) return
  try {
    phy.value = await api.post<Phy>('/phy/preview', { region: form.value.radio.region, preset: form.value.radio.preset, primary_channel: form.value.radio.primary_channel, tx_power_dbm: form.value.radio.tx_power_dbm })
  } catch {
    phy.value = null
  }
}
watch(() => form.value && [form.value.radio.region, form.value.radio.preset, form.value.radio.primary_channel, form.value.radio.tx_power_dbm], preview)
const freqChanged = computed(() => !!phy.value && !!live.status && Math.abs(phy.value.frequency_mhz - live.status.phy.frequency_mhz) > 1e-6)

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
  if (!(await confirmDialog({ title: 'Restore this backup?', body: `Config and identity keys from ${f.name} replace what is on this host now. Download a backup of the current state first if you might need it.`, confirm: 'Restore', danger: true }))) return
  restoring.value = true
  try {
    const r = await api.post<{ restart_required: boolean }>('/restore', body)
    restartRequired.value ||= r.restart_required
    toast('Backup restored')
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
    preview()
  } catch (e) {
    toastError(e)
  }
  api.get<SerialPort[]>('/serial-ports').then((p) => (ports.value = p), () => {})
  api.get<Region[]>('/regions').then((r) => (regions.value = r), () => {})
  loadTokens().catch(() => {})
})

const presetLabel = (p: string) => p.split('_').map((w) => w[0] + w.slice(1).toLowerCase()).join('')
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

    <div v-if="restartRequired" class="mb-4 flex items-center gap-2.5 rounded-xl border border-warn/30 bg-warn/10 px-4 py-2.5 text-[13px]">
      <TriangleAlert class="size-4 shrink-0 text-warn" />
      <span class="flex-1">Some saved changes need a restart to take effect.</span>
      <button class="btn btn-sm" :disabled="restarting" @click="restartNow"><RotateCcw class="size-3.5" />Restart now</button>
    </div>

    <section class="card overflow-hidden">
      <div class="tabs px-3 sm:px-4" role="tablist">
        <button v-for="t in tabs" :key="t.id" role="tab" :aria-selected="tab === t.id" @click="setTab(t.id)">{{ t.label }}</button>
      </div>

      <div v-if="!form" class="p-6"><div class="h-48 animate-pulse rounded-xl bg-sunken" /></div>

      <div v-else class="p-4 sm:p-6">
        <!-- RADIO -->
        <div v-if="tab === 'radio'" class="grid gap-6 lg:grid-cols-[1fr_20rem]">
          <div class="grid content-start gap-4 sm:grid-cols-2">
            <div class="sm:col-span-2">
              <label class="label" for="c-port">Modem serial port</label>
              <input id="c-port" v-model="form.radio.port" class="input mono" list="c-ports" />
              <datalist id="c-ports"><option v-for="p in ports" :key="p.path" :value="p.path">{{ p.description }}</option></datalist>
              <p class="hint">Driver <span class="mono">{{ form.radio.type }}</span>. Prefer <span class="mono">/dev/serial/by-id/…</span> so the path survives reboots. Changing it needs a restart.</p>
            </div>
            <div>
              <label class="label" for="c-region">Region</label>
              <select id="c-region" v-model="form.radio.region" class="input">
                <option v-for="r in regions" :key="r.name" :value="r.name">{{ r.name }}</option>
              </select>
            </div>
            <div>
              <label class="label" for="c-preset">Modem preset</label>
              <select id="c-preset" v-model="form.radio.preset" class="input">
                <option v-for="p in region?.presets ?? [form.radio.preset]" :key="p" :value="p">{{ presetLabel(p) }}</option>
              </select>
            </div>
            <div>
              <label class="label" for="c-primary">Primary channel name</label>
              <input id="c-primary" v-model="form.radio.primary_channel" class="input" maxlength="11" placeholder="empty = preset name" />
            </div>
            <div>
              <label class="label" for="c-offset">Frequency offset (MHz)</label>
              <input id="c-offset" v-model.number="form.radio.frequency_offset_mhz" type="number" step="0.001" class="input tabular-nums" />
            </div>
            <div class="sm:col-span-2">
              <label class="label" for="c-power">TX power · {{ form.radio.tx_power_dbm }} dBm <span class="font-normal text-ink-3">(region max {{ region?.power_limit_dbm ?? '…' }} dBm)</span></label>
              <input id="c-power" v-model.number="form.radio.tx_power_dbm" type="range" min="1" :max="region?.power_limit_dbm ?? 30" class="w-full accent-[var(--brand)]" />
            </div>
          </div>
          <aside class="rounded-xl border border-line-soft bg-raised p-4">
            <div class="eyebrow">Resulting PHY</div>
            <template v-if="phy">
              <div class="mt-2 text-[28px] font-semibold leading-none tracking-tight tabular-nums">{{ (phy.frequency_mhz + (form.radio.frequency_offset_mhz || 0)).toFixed(3) }}<span class="ml-1 text-sm font-normal text-ink-3">MHz</span></div>
              <div class="mt-1 text-xs text-ink-3">slot {{ phy.slot + 1 }}/{{ phy.num_slots }} · "{{ phy.primary_channel }}"</div>
              <dl class="kv mt-4 !grid-cols-[auto_1fr] text-xs">
                <dt>Bandwidth</dt><dd class="tabular-nums">{{ phy.bw_khz }} kHz</dd>
                <dt>Spreading factor</dt><dd>SF{{ phy.sf }}</dd>
                <dt>Coding rate</dt><dd>4/{{ phy.cr }}</dd>
                <dt>Sync word</dt><dd class="mono">0x{{ phy.sync_word.toString(16).toUpperCase() }}</dd>
                <dt>Preamble</dt><dd>{{ phy.preamble }} symbols</dd>
                <dt>TX power</dt><dd>{{ phy.tx_power_dbm }} dBm</dd>
              </dl>
              <p v-if="freqChanged" class="mt-3 rounded-lg bg-warn/10 px-2.5 py-2 text-xs text-warn">
                This moves the radio off {{ live.status?.phy.frequency_mhz.toFixed(3) }} MHz. Every identity will leave its current mesh.
              </p>
            </template>
            <div v-else class="mt-2 h-24 animate-pulse rounded-lg bg-sunken" />
          </aside>
        </div>

        <!-- RELAY -->
        <div v-else-if="tab === 'relay'" class="grid max-w-2xl gap-4 sm:grid-cols-[1fr_8rem]">
          <div class="sm:col-span-2">
            <span class="label">Role</span>
            <div class="seg">
              <button v-for="r in ['client', 'router', 'mute'] as const" :key="r" :aria-pressed="form.relay.role === r" class="capitalize" @click="form.relay.role = r">{{ r }}</button>
            </div>
            <p class="hint">Same switch as in the top bar. Router rebroadcasts first; client waits and cancels if someone else relays; mute never relays.</p>
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
            <label class="label" for="c-duty">Duty cycle limit · {{ form.airtime.duty_cycle_percent }}%</label>
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
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="label" for="c-pos">Position</label>
              <select id="c-pos" v-model="form.airtime.position" class="input">
                <option value="off">Off</option>
                <option value="fixed">Fixed</option>
              </select>
            </div>
            <div>
              <label class="label" for="c-tel">Telemetry</label>
              <select id="c-tel" v-model="form.airtime.telemetry" class="input">
                <option value="off">Off</option>
                <option value="device">Device</option>
              </select>
            </div>
          </div>
          <div class="sm:col-span-2">
            <h4 class="eyebrow mb-2 mt-2">Contention window</h4>
            <div class="grid grid-cols-2 gap-3 sm:max-w-sm">
              <div>
                <label class="label" for="c-cwmin">CW min</label>
                <input id="c-cwmin" v-model.number="form.airtime.cw_min" type="number" min="1" max="15" class="input tabular-nums" />
              </div>
              <div>
                <label class="label" for="c-cwmax">CW max</label>
                <input id="c-cwmax" v-model.number="form.airtime.cw_max" type="number" min="1" max="15" class="input tabular-nums" />
              </div>
            </div>
            <p class="hint">Firmware defaults are 3 and 8. Wider windows mean fewer collisions and higher latency.</p>
          </div>
        </div>

        <!-- POSITION & HARDWARE -->
        <div v-else-if="tab === 'position'" class="grid max-w-3xl gap-4 sm:grid-cols-2">
          <div class="sm:col-span-2">
            <h4 class="eyebrow mb-1">Site position</h4>
            <p class="hint !mt-0">A fixed location broadcast like a fixed node and answered on request. Both zero means no position.</p>
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
        <div v-else-if="tab === 'mqtt'" class="grid max-w-3xl gap-4 sm:grid-cols-2">
          <div class="flex items-center justify-between gap-3 rounded-xl border border-line-soft bg-raised px-3.5 py-3 sm:col-span-2">
            <div>
              <div class="text-[13px] font-medium">MQTT gateway</div>
              <div class="text-xs text-ink-3">The relay persona is the gateway. Which channels go up or down is set per identity channel.</div>
            </div>
            <input id="m-en" v-model="form.mqtt.enabled" type="checkbox" class="size-4 accent-[var(--brand)]" aria-label="Enable the MQTT gateway" />
          </div>
          <div>
            <label class="label" for="m-addr">Broker (host:port)</label>
            <input id="m-addr" v-model="form.mqtt.address" class="input mono" placeholder="mqtt.meshtastic.org:1883" />
          </div>
          <div>
            <label class="label" for="m-root">Root topic</label>
            <input id="m-root" v-model="form.mqtt.root" class="input mono" placeholder="msh/EU_868/Scotland" />
            <p class="hint">Empty uses <span class="mono">msh/&lt;region&gt;</span>.</p>
          </div>
          <div>
            <label class="label" for="m-user">Username</label>
            <input id="m-user" v-model="form.mqtt.username" class="input" autocomplete="off" />
          </div>
          <div>
            <label class="label" for="m-pass">Password</label>
            <input id="m-pass" v-model="form.mqtt.password" type="password" class="input" autocomplete="new-password" :placeholder="form.mqtt.password_set ? 'saved · leave empty to keep' : ''" />
          </div>
          <label class="flex items-center gap-2 text-[13px]"><input v-model="form.mqtt.tls" type="checkbox" class="size-4 accent-[var(--brand)]" /> TLS</label>
          <div>
            <label class="label" for="m-rate">Downlink limit · {{ form.mqtt.downlink_per_minute }}/min</label>
            <input id="m-rate" v-model.number="form.mqtt.downlink_per_minute" type="range" min="1" max="120" step="1" class="w-full accent-[var(--brand)]" />
          </div>
          <label class="flex items-start gap-2 text-[13px] sm:col-span-2">
            <input v-model="form.mqtt.ok_to_mqtt" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" />
            <span>OK to MQTT<span class="block text-xs text-ink-3">Other gateways may uplink the packets our identities send.</span></span>
          </label>
          <label class="flex items-start gap-2 text-[13px] sm:col-span-2">
            <input v-model="form.mqtt.relay_mqtt" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" />
            <span :class="form.mqtt.relay_mqtt ? 'text-warn' : ''">Relay MQTT traffic on air<span class="block text-xs text-ink-3">Off keeps broker traffic off the radio entirely (the firmware's “Ignore MQTT”). Leave off on a busy site.</span></span>
          </label>
          <div class="sm:col-span-2">
            <h4 class="eyebrow mb-1 mt-2">Map report</h4>
          </div>
          <label class="flex items-center gap-2 text-[13px]"><input v-model="form.mqtt.map_report.enabled" type="checkbox" class="size-4 accent-[var(--brand)]" /> Publish to the map topic</label>
          <div>
            <label class="label" for="m-mint">Interval</label>
            <select id="m-mint" v-model="form.mqtt.map_report.interval" class="input">
              <option v-for="v in ['15m', '30m', '1h', '3h', '6h']" :key="v" :value="v">every {{ v }}</option>
            </select>
          </div>
          <div class="sm:col-span-2">
            <label class="label" for="m-mbits">Map precision · {{ form.mqtt.map_report.position_precision === 32 ? 'exact' : `${form.mqtt.map_report.position_precision} bits` }}</label>
            <input id="m-mbits" v-model.number="form.mqtt.map_report.position_precision" type="range" min="10" max="32" step="1" class="w-full accent-[var(--brand)]" />
            <p class="hint">Uses the site position from Position &amp; hardware. Public brokers coarsen positions whatever you send.</p>
          </div>
          <p class="hint sm:col-span-2">Broker and map report changes take effect after a restart.</p>
        </div>

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
            </div>
            <form class="rounded-xl border border-line-soft p-4" @submit.prevent="changePassword">
              <h4 class="card-title mb-3">Change admin password</h4>
              <div class="grid gap-3 sm:grid-cols-3">
                <input v-model="pw.current" type="password" class="input" placeholder="Current" autocomplete="current-password" />
                <input v-model="pw.next" type="password" class="input" placeholder="New (8+ chars)" autocomplete="new-password" />
                <input v-model="pw.repeat" type="password" class="input" placeholder="Repeat new" autocomplete="new-password" />
              </div>
              <div class="mt-3 flex items-center justify-between gap-2">
                <span v-if="pw.repeat && pw.next !== pw.repeat" class="text-xs text-bad">Passwords don't match</span><span v-else />
                <button class="btn btn-sm" :disabled="pwBusy || !pw.current || pw.next.length < 8 || pw.next !== pw.repeat"><Spinner v-if="pwBusy" />Change password</button>
              </div>
            </form>
          </div>

          <div>
            <h4 class="card-title">API tokens</h4>
            <p class="mb-3 mt-0.5 text-xs text-ink-3">For Home Assistant, scripts and Prometheus. A token is shown once when created.</p>
            <form class="mb-3 flex gap-2" @submit.prevent="createToken">
              <input v-model="newTokenName" class="input" placeholder="Token name, e.g. Home Assistant" maxlength="40" />
              <button class="btn btn-primary shrink-0" :disabled="!newTokenName.trim()"><Plus class="size-4" />Create</button>
            </form>
            <div class="overflow-hidden rounded-xl border border-line-soft">
              <div v-for="t in tokens" :key="t.id" class="flex items-center gap-3 border-b border-line-soft px-3.5 py-2.5 last:border-b-0">
                <KeyRound class="size-4 shrink-0 text-ink-3" />
                <div class="min-w-0 flex-1">
                  <div class="truncate text-[13px] font-medium">{{ t.name }}</div>
                  <div class="text-2xs text-ink-3">created {{ relTime(t.created_at, now) }} · {{ t.last_used ? `last used ${relTime(t.last_used, now)}` : 'never used' }}</div>
                </div>
                <button class="icon-btn hover:!text-bad" title="Revoke" @click="revoke(t)"><Trash class="size-4" /></button>
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
            <p class="mt-1 text-[13px] text-ink-3">One JSON file with the config and every identity's private key. Store it like a password.</p>
            <button class="btn mt-4" :disabled="downloading" @click="download"><Spinner v-if="downloading" /><Download v-else class="size-4" />Download backup</button>
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
              <button class="btn btn-danger" :disabled="!restoreFile || restoring" @click="restore"><Spinner v-if="restoring" />Restore</button>
            </div>
          </div>
        </div>
      </div>

      <div v-if="form && section" class="flex items-center justify-end gap-2 border-t border-line-soft bg-raised/60 px-4 py-3 sm:px-6">
        <span v-if="dirty" class="mr-auto text-xs text-warn">Unsaved changes</span>
        <button class="btn btn-sm" :disabled="!dirty" @click="revert"><RotateCcw class="size-3.5" />Revert</button>
        <button class="btn btn-sm btn-primary" :disabled="!dirty || saving" @click="save"><Spinner v-if="saving" />Save changes</button>
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
      <template #footer><button class="btn btn-primary" @click="created = null">I've copied it</button></template>
    </Modal>
  </div>
</template>
