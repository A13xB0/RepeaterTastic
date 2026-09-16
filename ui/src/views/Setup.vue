<script setup lang="ts">
// Setup wizard: radio → meshtasticd → region → relay → admin password → review.
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Check, CircleAlert, CircuitBoard, CircleCheck, RefreshCw, Server, Usb } from '@lucide/vue'
import { request, setToken } from '@/api/client'
import type { Board, Phy, ProbeResult, RelayRole, Region, SerialPort } from '@/api/types'
import BoardSelect from '@/components/config/BoardSelect.vue'
import Logo from '@/components/ui/Logo.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { markSetupDone } from '@/router'
import { num } from '@/lib/format'

const router = useRouter()
const stepList = [
  { id: 'radio', label: 'Radio' },
  { id: 'nodes', label: 'meshtasticd' },
  { id: 'region', label: 'Region' },
  { id: 'relay', label: 'Relay' },
  { id: 'password', label: 'Password' },
  { id: 'review', label: 'Review' },
] as const
const steps = stepList.map((x) => x.label)
const step = ref(0)
const stepId = computed(() => stepList[step.value].id)

// Hosted nodes (experimental): the relay persona runs on meshtasticd.
const DEFAULT_IMAGE = 'meshtastic/meshtasticd:2.8.0.47db0e3-alpha-debian'
const hosted = ref(false)
const hostedVia = ref<'exec' | 'docker'>('exec')
const hostedBinary = ref('')
const hostedImage = ref(DEFAULT_IMAGE)
const hostedPortBase = ref(4500)
const hostedCheck = ref<{ ok: boolean; version: string; min_version: string; launcher: string; error: string } | null>(null)
const checkingHosted = ref(false)
async function checkHosted() {
  checkingHosted.value = true
  hostedCheck.value = null
  try {
    hostedCheck.value = await post('/setup/meshtasticd', hostedVia.value === 'docker' ? { docker_image: hostedImage.value } : { meshtasticd: hostedBinary.value })
  } catch (e) {
    hostedCheck.value = { ok: false, version: '', min_version: '2.8.0', launcher: '', error: (e as Error).message }
  } finally {
    checkingHosted.value = false
  }
}
watch([hostedVia, hostedBinary, hostedImage], () => (hostedCheck.value = null))
function hostedSettings() {
  return {
    persona: hosted.value,
    meshtasticd: hostedVia.value === 'exec' ? hostedBinary.value.trim() : '',
    docker_image: hostedVia.value === 'docker' ? hostedImage.value.trim() : '',
    port_base: hostedPortBase.value,
  }
}

const ports = ref<SerialPort[]>([])
const loadingPorts = ref(false)
const device = ref('')
// kiss: a USB modem on the serial port `device`; spi: `device` is a LoRa board the daemon drives itself.
const driver = ref<'kiss' | 'spi'>('kiss')
const probe = ref<ProbeResult | null>(null)
const probing = ref(false)

const regions = ref<Region[]>([])
const region = ref('EU_868')
const preset = ref('LONG_FAST')
const primary = ref('')
const phy = ref<Phy | null>(null)
const phyError = ref('')

const role = ref<RelayRole>('client')
const password = ref('')
const password2 = ref('')
const error = ref('')
const finishing = ref(false)

const post = <T,>(p: string, b: unknown) => request<T>('POST', p, b, { auth: false })
const get = <T,>(p: string) => request<T>('GET', p, undefined, { auth: false })

async function loadPorts() {
  loadingPorts.value = true
  try {
    ports.value = await get<SerialPort[]>('/serial-ports')
    if (!device.value && ports.value[0]) device.value = ports.value[0].path
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loadingPorts.value = false
  }
}

async function runProbe() {
  if (!device.value) return
  probing.value = true
  probe.value = null
  try {
    probe.value = await post<ProbeResult>('/setup/probe', { device: device.value, driver: driver.value })
  } catch (e) {
    probe.value = { ok: false, driver: '', firmware: '', name: '', sync_word_ok: false, error: (e as Error).message }
  } finally {
    probing.value = false
  }
}
watch([device, driver], () => (probe.value = null))

// A LoRa board on the Pi's SPI bus or a CH341 USB stick, run by the experimental spi driver.
const boards = ref<Board[]>([])
const board = ref('auto')
const usingBoard = computed(() => driver.value === 'spi')
function pickBoard() {
  driver.value = 'spi'
  device.value = board.value
}
watch(board, (v) => {
  if (usingBoard.value) device.value = v
})

// A serial port typed by hand, for one the daemon didn't list.
const manualPort = ref('')
const usingManual = ref(false)
watch(manualPort, (v) => {
  if (usingManual.value) device.value = v.trim()
})
function pickManual() {
  usingManual.value = true
  driver.value = 'kiss'
  device.value = manualPort.value.trim()
}
watch(device, (v) => {
  // picking a listed port or a board leaves manual mode
  if (usingManual.value && v !== manualPort.value.trim()) usingManual.value = false
})

let previewSeq = 0
async function preview() {
  const seq = ++previewSeq
  phyError.value = ''
  try {
    const r = await post<Phy>('/phy/preview', { region: region.value, preset: preset.value, primary_channel: primary.value })
    if (seq === previewSeq) phy.value = r
  } catch (e) {
    if (seq === previewSeq) phyError.value = (e as Error).message
  }
}
watch([region, preset, primary], preview)

onMounted(async () => {
  loadPorts()
  get<Board[]>('/boards')
    .then((b) => (boards.value = b))
    .catch(() => {
      /* the board select still offers auto and a file path */
    })
  try {
    regions.value = await get<Region[]>('/regions')
  } catch {
    /* shown as empty select */
  }
  preview()
})

const regionInfo = computed(() => regions.value.find((r) => r.name === region.value))
const presetLabel = (p: string) => p.split('_').map((w) => w[0] + w.slice(1).toLowerCase()).join('')

const roles: { id: RelayRole; title: string; body: string }[] = [
  { id: 'client', title: 'Client', body: 'Rebroadcast what we hear, but wait for routers and cancel if another node relays first. Right for most homes.' },
  { id: 'client_base', title: 'Client base', body: 'A client that relays for its favourited nodes with router priority. For a base station serving your own nodes.' },
  { id: 'client_mute', title: 'Client mute', body: 'Never rebroadcast. Your identities still send and receive normally.' },
  { id: 'router', title: 'Router', body: 'Always rebroadcast, with priority. Only for a well-placed site (rooftop, hill) that the mesh relies on.' },
  { id: 'router_late', title: 'Router late', body: 'Always rebroadcast, but only after other nodes had their chance. Fills gaps without taking over.' },
  { id: 'monitor', title: 'Monitor', body: 'Listen only. Nothing is transmitted, not even by your identities: no messages, ACKs, NodeInfo or telemetry.' },
  { id: 'off', title: 'Off', body: 'The radio is ignored: nothing received or sent. Local DMs, links and apps still work.' },
]

const pwScore = computed(() => {
  const p = password.value
  let s = 0
  if (p.length >= 8) s++
  if (p.length >= 12) s++
  if (/[A-Z]/.test(p) && /[a-z]/.test(p)) s++
  if (/\d/.test(p) || /[^\w]/.test(p)) s++
  return s
})

const canNext = computed(() => {
  switch (stepId.value) {
    case 'radio': return !!device.value
    case 'nodes': return !hosted.value || !!hostedCheck.value?.ok
    case 'region': return !!phy.value && !phyError.value
    case 'password': return password.value.length >= 8 && password.value === password2.value
    default: return true
  }
})

async function waitForRestart() {
  await new Promise((r) => setTimeout(r, 1500))
  for (let i = 0; i < 60; i++) {
    try {
      await request('GET', '/status')
      return
    } catch {
      await new Promise((r) => setTimeout(r, 750))
    }
  }
}

async function finish() {
  finishing.value = true
  error.value = ''
  try {
    const r = await post<{ token: string; restart_required: boolean }>('/setup', {
      password: password.value, region: region.value, preset: preset.value, primary_channel: primary.value, driver: driver.value, device: device.value, relay_role: role.value,
      hosted: hostedSettings(),
    })
    setToken(r.token)
    // The relay moves to meshtasticd when the daemon starts again.
    if (r.restart_required && hosted.value) {
      await request('POST', '/restart').catch(() => {})
      await waitForRestart()
    }
    markSetupDone()
    router.replace('/')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    finishing.value = false
  }
}
</script>

<template>
  <div class="app-bg min-h-dvh px-4 py-8 sm:py-12">
    <div class="mx-auto w-full max-w-2xl">
      <div class="mb-6 flex items-center gap-3">
        <Logo :size="44" />
        <div>
          <h1 class="text-lg font-bold tracking-tight">Welcome to Repeater<span class="text-brand">Tastic</span></h1>
          <p class="text-[13px] text-ink-3">A few questions and your modem is on the mesh.</p>
        </div>
      </div>

      <ol class="mb-4 flex items-center gap-1.5 overflow-x-auto pb-1">
        <li v-for="(label, i) in steps" :key="label" class="flex shrink-0 items-center gap-1.5">
          <button
            :disabled="i > step"
            :class="['flex items-center gap-2 rounded-full py-1 pl-1 pr-3 text-xs font-medium transition-colors', i === step ? 'bg-brand/12 text-ink' : i < step ? 'text-ink-2 hover:bg-sunken' : 'text-ink-3']"
            @click="step = i"
          >
            <span :class="['flex size-5 items-center justify-center rounded-full text-2xs font-bold', i < step ? 'bg-brand text-brand-ink' : i === step ? 'bg-brand text-brand-ink' : 'bg-sunken text-ink-3']">
              <Check v-if="i < step" class="size-3" /><template v-else>{{ i + 1 }}</template>
            </span>
            {{ label }}
          </button>
          <span v-if="i < steps.length - 1" class="h-px w-4 bg-line" />
        </li>
      </ol>

      <div class="card !bg-surface-solid/90">
        <div class="px-5 py-5 sm:px-7 sm:py-6">
          <!-- Radio -->
          <section v-if="stepId === 'radio'">
            <h2 class="text-base font-semibold tracking-tight">Connect the radio</h2>
            <p class="mt-1 text-[13px] text-ink-3">
              Pick the serial port of your KISS modem (a Heltec V3 or RAK4631 with the RepeaterTastic sync-word patch) or a LoRa board RepeaterTastic drives itself. We'll ping it and check it accepts sync word 0x2B.
            </p>
            <p class="mt-2 flex flex-wrap items-center gap-1.5 text-2xs text-ink-3">
              <span class="chip bg-brand/12 text-brand">0 hops</span>
              The relay and your identities all transmit on this radio themselves.
            </p>
            <div class="mt-4 space-y-2">
              <label
                v-for="p in ports"
                :key="p.path"
                :class="['flex cursor-pointer items-center gap-3 rounded-xl border px-3.5 py-3 transition-colors', device === p.path ? 'border-brand/60 bg-brand/6' : 'border-line hover:bg-raised']"
              >
                <input v-model="device" type="radio" :value="p.path" class="accent-[var(--brand)]" @change="driver = 'kiss'" />
                <Usb class="size-4 shrink-0 text-ink-3" />
                <div class="min-w-0">
                  <div class="text-[13px] font-medium">{{ p.description }}</div>
                  <div class="mono truncate text-2xs text-ink-3">{{ p.path }}</div>
                </div>
              </label>
              <div v-if="!ports.length && !loadingPorts" class="rounded-xl border border-dashed border-line px-4 py-6 text-center text-[13px] text-ink-3">
                No serial ports found. Plug the modem in and refresh.
              </div>
              <div :class="['rounded-xl border px-3.5 py-3 transition-colors', usingManual ? 'border-brand/60 bg-brand/6' : 'border-line hover:bg-raised']">
                <label class="flex cursor-pointer items-center gap-3">
                  <input type="radio" :checked="usingManual" class="accent-[var(--brand)]" @change="pickManual" />
                  <Usb class="size-4 shrink-0 text-ink-3" />
                  <div class="min-w-0">
                    <div class="text-[13px] font-medium">Serial port by name</div>
                    <div class="text-2xs text-ink-3">A KISS modem on a port that isn't listed: <span class="mono">/dev/ttyUSB0</span>, <span class="mono">/dev/serial/by-id/…</span> or a udev alias</div>
                  </div>
                </label>
                <input v-if="usingManual" id="setup-serial" v-model="manualPort" class="input h-8 mono mt-2.5 ml-7 w-[calc(100%-1.75rem)] text-xs" placeholder="/dev/ttyUSB0" spellcheck="false" aria-label="Serial port" />
              </div>
              <div :class="['rounded-xl border px-3.5 py-3 transition-colors', usingBoard ? 'border-brand/60 bg-brand/6' : 'border-line hover:bg-raised']">
                <label class="flex cursor-pointer items-center gap-3">
                  <input type="radio" :checked="usingBoard" class="accent-[var(--brand)]" @change="pickBoard" />
                  <CircuitBoard class="size-4 shrink-0 text-ink-3" />
                  <div class="min-w-0">
                    <div class="text-[13px] font-medium">LoRa board on SPI or a USB stick</div>
                    <div class="text-2xs text-ink-3">A Pi HAT or CH341 stick RepeaterTastic drives itself, with no meshtasticd on it (experimental)</div>
                  </div>
                </label>
                <BoardSelect v-if="usingBoard" id="setup-board" v-model="board" :boards="boards" input-class="input h-8 text-xs" class="mt-2.5 pl-7" />
              </div>
            </div>
            <div class="mt-3 flex flex-wrap items-center gap-2">
              <button class="btn btn-sm" :disabled="loadingPorts" @click="loadPorts"><RefreshCw :class="['size-3.5', loadingPorts && 'animate-spin']" />Refresh</button>
              <button class="btn btn-sm" :disabled="!device || probing" @click="runProbe"><Spinner v-if="probing" />Test modem</button>

            </div>
            <div v-if="probe" :class="['mt-3 flex items-start gap-2.5 rounded-xl border px-3.5 py-3 text-[13px]', probe.ok ? 'border-ok/30 bg-ok/8' : 'border-bad/30 bg-bad/8']">
              <CircleCheck v-if="probe.ok" class="mt-0.5 size-4 shrink-0 text-ok" />
              <CircleAlert v-else class="mt-0.5 size-4 shrink-0 text-bad" />
              <div v-if="probe.ok" class="min-w-0">
                <div class="font-medium">{{ probe.name }} answered</div>
                <div v-if="probe.driver === 'spi'" class="text-ink-2">{{ probe.firmware }} module · ready for sync word 0x2B</div>
                <div v-else class="text-ink-2">{{ probe.firmware }} · sync word 0x2B {{ probe.sync_word_ok ? 'accepted' : 'rejected (flash the patched firmware)' }}</div>
                <ul v-if="probe.details?.length" class="mono mt-1.5 space-y-0.5 text-2xs text-ink-3">
                  <li v-for="(d, i) in probe.details" :key="i" class="break-words">{{ d }}</li>
                </ul>
              </div>
              <div v-else>
                <div class="font-medium">{{ usingBoard ? 'The board didn’t answer' : 'No modem on this port' }}</div>
                <div class="text-ink-2">{{ probe.error }}</div>
              </div>
            </div>
          </section>

          <!-- meshtasticd -->
          <section v-else-if="stepId === 'nodes'">
            <h2 class="text-base font-semibold tracking-tight">Run nodes on meshtasticd</h2>
            <p class="mt-1 text-[13px] text-ink-3">
              Experimental: the relay persona can be a real Meshtastic node, a meshtasticd on a simulated radio that RepeaterTastic starts, sets up and restarts.
              It still transmits on this radio itself, at zero hops.
            </p>
            <div :class="['mt-4 rounded-xl border px-3.5 py-3', hosted ? 'border-brand/60 bg-brand/6' : 'border-line']">
              <label class="flex cursor-pointer items-center gap-3">
                <input id="setup-hosted" v-model="hosted" type="checkbox" class="accent-[var(--brand)]" />
                <Server class="size-4 shrink-0 text-ink-3" />
                <div class="min-w-0">
                  <div class="text-[13px] font-medium">Run the relay on meshtasticd</div>
                  <div class="text-2xs text-ink-3">Needs meshtasticd 2.8.0 or newer, installed or in Docker. Your identities stay in RepeaterTastic for now.</div>
                </div>
              </label>
              <div v-if="hosted" class="mt-3 space-y-3 pl-7">
                <div class="flex flex-wrap items-center gap-2">
                  <div class="seg" role="group" aria-label="How to run meshtasticd">
                    <button type="button" :aria-pressed="hostedVia === 'exec'" @click="hostedVia = 'exec'">Installed</button>
                    <button type="button" :aria-pressed="hostedVia === 'docker'" @click="hostedVia = 'docker'">Docker</button>
                  </div>
                  <input v-if="hostedVia === 'exec'" id="setup-hosted-bin" v-model="hostedBinary" class="input h-8 mono min-w-0 flex-1 text-xs" placeholder="meshtasticd (on PATH) or /usr/bin/meshtasticd" spellcheck="false" aria-label="meshtasticd program" />
                  <input v-else id="setup-hosted-image" v-model="hostedImage" class="input h-8 mono min-w-0 flex-1 text-xs" spellcheck="false" aria-label="meshtasticd image" />
                  <button class="btn btn-sm" :disabled="checkingHosted" @click="checkHosted"><Spinner v-if="checkingHosted" />Check</button>
                </div>
                <div v-if="hostedCheck" :class="['flex items-start gap-2.5 rounded-xl border px-3.5 py-2.5 text-[13px]', hostedCheck.ok ? 'border-ok/30 bg-ok/8' : 'border-bad/30 bg-bad/8']">
                  <CircleCheck v-if="hostedCheck.ok" class="mt-0.5 size-4 shrink-0 text-ok" />
                  <CircleAlert v-else class="mt-0.5 size-4 shrink-0 text-bad" />
                  <div v-if="hostedCheck.ok"><div class="font-medium">meshtasticd {{ hostedCheck.version }} found</div><div class="text-ink-2">{{ hostedCheck.min_version }} or newer is needed · {{ hostedCheck.launcher }}</div></div>
                  <div v-else><div class="font-medium">meshtasticd can't host nodes</div><div class="text-ink-2">{{ hostedCheck.error }}</div></div>
                </div>
                <div class="grid gap-3 sm:grid-cols-2">
                  <div>
                    <label class="label" for="setup-hosted-port">API ports from</label>
                    <input id="setup-hosted-port" v-model.number="hostedPortBase" type="number" min="1024" max="64000" class="input h-8 text-xs" />
                  </div>
                  <p class="hint self-end">About 3 MB of memory per node. <template v-if="hostedVia === 'exec'">meshtasticd listens on every interface: firewall ports {{ hostedPortBase }}–{{ hostedPortBase + 19 }} on a shared network, or use Docker.</template><template v-else>Docker keeps the ports on this machine.</template></p>
                </div>
              </div>
            </div>
            <div class="mt-4 rounded-xl border border-line-soft bg-raised p-4">
              <div class="flex items-center justify-between gap-2">
                <div class="eyebrow">What will run on this radio</div>
                <span class="chip bg-brand/12 text-brand">all at 0 hops</span>
              </div>
              <table class="mt-2 w-full text-xs">
                <tbody>
                  <tr class="border-b border-line-soft"><td class="py-1.5 font-medium">Relay persona</td><td class="py-1.5 text-ink-3">{{ hosted ? (hostedCheck?.ok ? `meshtasticd ${hostedCheck.version}` : 'meshtasticd') : 'RepeaterTastic' }}</td><td class="py-1.5 text-right"><span class="chip bg-brand/12 text-brand">0 hops</span></td></tr>
                  <tr><td class="py-1.5 font-medium">Your identities</td><td class="py-1.5 text-ink-3">RepeaterTastic</td><td class="py-1.5 text-right"><span class="chip bg-brand/12 text-brand">0 hops</span></td></tr>
                </tbody>
              </table>
              <p class="mt-2 text-2xs text-ink-3">Everything on this radio transmits from here. The relay hears your identities but never repeats them: they already went out from this mast.</p>
            </div>
          </section>

          <!-- Region -->
          <section v-else-if="stepId === 'region'">
            <h2 class="text-base font-semibold tracking-tight">Region and preset</h2>
            <p class="mt-1 text-[13px] text-ink-3">These must match the mesh you want to join. Most UK and EU meshes use EU_868 with LongFast.</p>
            <div class="mt-4 grid gap-4 sm:grid-cols-2">
              <div>
                <label class="label" for="region">Region</label>
                <select id="region" v-model="region" class="input">
                  <option v-for="r in regions" :key="r.name" :value="r.name">{{ r.name }}</option>
                </select>
                <p v-if="regionInfo" class="hint">Duty cycle {{ num(regionInfo.duty_cycle_pct) }}% · max {{ regionInfo.power_limit_dbm }} dBm</p>
              </div>
              <div>
                <label class="label" for="primary">Primary channel name</label>
                <input id="primary" v-model="primary" class="input" placeholder="empty = preset name" maxlength="11" />
                <p class="hint">Shared by every identity; changes the frequency slot.</p>
              </div>
            </div>
            <div class="mt-4">
              <span class="label">Preset</span>
              <div class="grid grid-cols-2 gap-1.5 sm:grid-cols-4">
                <button
                  v-for="p in regionInfo?.presets ?? ['LONG_FAST']"
                  :key="p"
                  :class="['rounded-lg border px-2 py-1.5 text-xs font-medium transition-colors', preset === p ? 'border-brand/60 bg-brand/10 text-ink' : 'border-line text-ink-2 hover:bg-raised']"
                  @click="preset = p"
                >
                  {{ presetLabel(p) }}
                </button>
              </div>
            </div>
            <div class="mt-5 rounded-xl border border-line-soft bg-raised p-4">
              <div class="eyebrow">Computed radio settings</div>
              <p v-if="phyError" class="mt-2 text-[13px] text-bad">{{ phyError }}</p>
              <div v-else-if="phy" class="mt-2 flex flex-wrap items-end gap-x-8 gap-y-3">
                <div>
                  <div class="text-[28px] font-semibold leading-none tracking-tight tabular-nums">{{ phy.frequency_mhz.toFixed(3) }}<span class="ml-1 text-sm font-normal text-ink-3">MHz</span></div>
                  <div class="mt-1 text-xs text-ink-3">slot {{ phy.slot + 1 }} of {{ phy.num_slots }} · channel "{{ phy.primary_channel }}"</div>
                </div>
                <dl class="grid grid-cols-3 gap-x-6 gap-y-1 text-xs">
                  <div><dt class="text-ink-3">Bandwidth</dt><dd class="font-medium tabular-nums">{{ num(phy.bw_khz) }} kHz</dd></div>
                  <div><dt class="text-ink-3">Spreading</dt><dd class="font-medium">SF{{ phy.sf }}</dd></div>
                  <div><dt class="text-ink-3">Coding</dt><dd class="font-medium">4/{{ phy.cr }}</dd></div>
                  <div><dt class="text-ink-3">Sync word</dt><dd class="mono font-medium">0x{{ phy.sync_word.toString(16).toUpperCase() }}</dd></div>
                  <div><dt class="text-ink-3">Preamble</dt><dd class="font-medium">{{ phy.preamble }} sym</dd></div>
                  <div><dt class="text-ink-3">TX power</dt><dd class="font-medium">{{ phy.tx_power_dbm }} dBm</dd></div>
                </dl>
              </div>
              <div v-else class="mt-2 h-12 animate-pulse rounded-lg bg-sunken" />
            </div>
          </section>

          <!-- Relay -->
          <section v-else-if="stepId === 'relay'">
            <h2 class="text-base font-semibold tracking-tight">Relay role</h2>
            <p class="mt-1 text-[13px] text-ink-3">
              The relay persona rebroadcasts other people's packets. You can change this any time from the top bar.
            </p>
            <div class="mt-4 grid gap-2 sm:grid-cols-3">
              <button
                v-for="r in roles"
                :id="`role-${r.id}`"
                :key="r.id"
                :class="['rounded-xl border p-3.5 text-left transition-colors', role === r.id ? 'border-brand/60 bg-brand/8' : 'border-line hover:bg-raised']"
                @click="role = r.id"
              >
                <div class="flex items-center justify-between">
                  <span class="text-[13px] font-semibold">{{ r.title }}</span>
                  <span :class="['flex size-4 items-center justify-center rounded-full border', role === r.id ? 'border-brand bg-brand text-brand-ink' : 'border-line']">
                    <Check v-if="role === r.id" class="size-2.5" />
                  </span>
                </div>
                <p class="mt-1.5 text-xs leading-relaxed text-ink-3">{{ r.body }}</p>
              </button>
            </div>
          </section>

          <!-- Password -->
          <section v-else-if="stepId === 'password'">
            <h2 class="text-base font-semibold tracking-tight">Admin password</h2>
            <p class="mt-1 text-[13px] text-ink-3">Protects this dashboard, including private keys. At least 8 characters.</p>
            <div class="mt-4 grid gap-4 sm:grid-cols-2">
              <div>
                <label class="label" for="pw1">Password</label>
                <input id="pw1" v-model="password" type="password" class="input" autocomplete="new-password" />
                <div class="mt-2 flex gap-1">
                  <span v-for="i in 4" :key="i" :class="['h-1 flex-1 rounded-full', i <= pwScore ? (pwScore >= 3 ? 'bg-ok' : pwScore === 2 ? 'bg-warn' : 'bg-bad') : 'bg-ink-3/15']" />
                </div>
              </div>
              <div>
                <label class="label" for="pw2">Repeat password</label>
                <input id="pw2" v-model="password2" type="password" class="input" autocomplete="new-password" />
                <p v-if="password2 && password !== password2" class="hint !text-bad">Passwords don't match</p>
              </div>
            </div>
          </section>

          <!-- Review -->
          <section v-else>
            <h2 class="text-base font-semibold tracking-tight">Ready to go</h2>
            <dl class="kv mt-4">
              <dt>Radio</dt><dd class="mono truncate">{{ usingBoard ? `board · ${device}` : device }}</dd>
              <dt>Region</dt><dd>{{ region }} · {{ phy?.preset_name }} · {{ phy?.frequency_mhz.toFixed(3) }} MHz</dd>
              <dt>Relay role</dt><dd class="capitalize">{{ role }}</dd>
              <dt>Relay runs on</dt><dd>{{ hosted ? `meshtasticd ${hostedCheck?.version ?? ''} (${hostedVia === 'docker' ? 'Docker' : 'installed'})` : 'RepeaterTastic' }}</dd>
              <dt>Admin password</dt><dd>{{ '•'.repeat(Math.min(password.length, 16)) }}</dd>
            </dl>
            <p v-if="hosted" class="mt-4 text-[13px] text-ink-3">RepeaterTastic restarts once to start the relay on meshtasticd, then opens the dashboard.</p>
            <p v-else class="mt-4 text-[13px] text-ink-3">Next you'll land on the dashboard, where you can create your first identity.</p>
            <p v-if="error" class="mt-3 text-[13px] text-bad">{{ error }}</p>
          </section>
        </div>

        <div class="flex items-center justify-between gap-2 border-t border-line-soft px-5 py-3 sm:px-7">
          <button class="btn btn-ghost" :disabled="step === 0" @click="step--">Back</button>
          <button v-if="step < steps.length - 1" class="btn btn-primary" :disabled="!canNext" @click="step++">Continue</button>
          <button v-else class="btn btn-primary" :disabled="finishing" @click="finish"><Spinner v-if="finishing" />Finish setup</button>
        </div>
      </div>
    </div>
  </div>
</template>
