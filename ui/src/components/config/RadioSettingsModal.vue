<script setup lang="ts">
// A running radio's name and LoRa & modem settings, edited in a modal like everything else on the
// Radios tab. Applies live where the daemon can (preset, power, channel); the modem connection
// needs a restart.
import { computed, ref, watch } from 'vue'
import { api, enc } from '@/api/client'
import type { Config, ConfigPutResult, Phy, Region, SerialPort } from '@/api/types'
import ModemDeviceField from '@/components/config/ModemDeviceField.vue'
import RadioNodesPanel from '@/components/config/RadioNodesPanel.vue'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { live, refreshRadios, refreshStatus } from '@/store/live'
import { toast } from '@/composables/toast'
import { num } from '@/lib/format'

const props = defineProps<{ radioId: string | null; ports: SerialPort[]; regions: Region[] }>()
const emit = defineEmits<{ close: [] }>()

const summary = computed(() => live.radios.find((r) => r.id === props.radioId))
const name = ref('')
const saved = ref<Config['radio'] | null>(null)
const form = ref<Config['radio'] | null>(null)
const phy = ref<Phy | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref('')

watch(
  () => props.radioId,
  async (id) => {
    form.value = saved.value = null
    phy.value = null
    error.value = ''
    if (!id) return
    name.value = summary.value?.name ?? id
    loading.value = true
    try {
      const c = await api.get<Config>(`/config?radio=${enc(id)}`)
      saved.value = c.radio
      form.value = structuredClone(c.radio)
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  },
)

const region = computed(() => props.regions.find((r) => r.name === form.value?.region))
const presetLabel = (p: string) => p.split('_').map((w) => w[0] + w.slice(1).toLowerCase()).join('')

watch(
  () => form.value && [form.value.region, form.value.preset, form.value.primary_channel, form.value.tx_power_dbm],
  async () => {
    if (!form.value) return
    try {
      phy.value = await api.post<Phy>('/phy/preview', { region: form.value.region, preset: form.value.preset, primary_channel: form.value.primary_channel, tx_power_dbm: form.value.tx_power_dbm })
    } catch {
      phy.value = null
    }
  },
)
const freqChanged = computed(() => !!phy.value && !!summary.value && Math.abs(phy.value.frequency_mhz - summary.value.phy.frequency_mhz) > 1e-6)
const dirty = computed(() => (form.value && JSON.stringify(form.value) !== JSON.stringify(saved.value)) || (summary.value && name.value.trim() !== summary.value.name))

async function save() {
  if (!props.radioId || !form.value) return
  saving.value = true
  error.value = ''
  try {
    let restart = false
    if (summary.value && name.value.trim() !== summary.value.name) await api.patch(`/radios/${enc(props.radioId)}`, { name: name.value.trim() })
    if (JSON.stringify(form.value) !== JSON.stringify(saved.value)) {
      const r = await api.put<ConfigPutResult>(`/config?radio=${enc(props.radioId)}`, { radio: form.value })
      restart = r.restart_required
    }
    await Promise.all([refreshRadios(), refreshStatus().catch(() => {})])
    toast(restart ? `${name.value.trim() || props.radioId} saved. Restart to apply the modem change.` : `${name.value.trim() || props.radioId} saved and applied`)
    emit('close')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Modal :open="!!radioId" :title="`Edit ${summary?.name ?? radioId ?? ''}`" subtitle="LoRa & modem. Relay, Airtime, Position and MQTT follow the radio picked at the top of the page." size="xl" @close="emit('close')">
    <div v-if="loading || !form" class="h-64 animate-pulse rounded-xl bg-sunken" />
    <div v-else class="grid gap-6 lg:grid-cols-[1fr_17rem]">
      <div class="grid content-start gap-4 sm:grid-cols-2">
        <div>
          <label class="label" for="rs-name">Name</label>
          <input id="rs-name" v-model="name" class="input" maxlength="40" />
        </div>
        <div>
          <label class="label" for="rs-id">ID</label>
          <input id="rs-id" :value="radioId" class="input mono" readonly />
        </div>
        <div class="sm:col-span-2">
          <ModemDeviceField id="rs-port" v-model="form.port" v-model:driver="form.type" :ports="ports" restart-hint />
        </div>
        <p v-if="form.type === 'meshtastic'" class="rounded-lg border border-brand/30 bg-brand/6 px-3 py-2 text-xs text-ink-2 sm:col-span-2">
          These settings are written to the board: region, preset, primary channel, TX power and hop limit, and the relay role. The board may reboot to apply them.
        </p>
        <div>
          <label class="label" for="rs-region">Region</label>
          <select id="rs-region" v-model="form.region" class="input">
            <option v-for="r in regions" :key="r.name" :value="r.name">{{ r.name }}</option>
          </select>
        </div>
        <div>
          <label class="label" for="rs-preset">Modem preset</label>
          <select id="rs-preset" v-model="form.preset" class="input">
            <option v-for="p in region?.presets ?? [form.preset]" :key="p" :value="p">{{ presetLabel(p) }}</option>
          </select>
        </div>
        <div>
          <label class="label" for="rs-primary">Primary channel name</label>
          <input id="rs-primary" v-model="form.primary_channel" class="input" maxlength="11" placeholder="empty = preset name" />
        </div>
        <div>
          <label class="label" for="rs-offset">Frequency offset (MHz)</label>
          <input id="rs-offset" v-model.number="form.frequency_offset_mhz" type="number" step="0.001" class="input tabular-nums" />
        </div>
        <div class="sm:col-span-2">
          <label class="label" for="rs-power">TX power · {{ form.tx_power_dbm }} dBm <span class="font-normal text-ink-3">(region max {{ region?.power_limit_dbm ?? '…' }} dBm)</span></label>
          <input id="rs-power" v-model.number="form.tx_power_dbm" type="range" min="1" :max="region?.power_limit_dbm ?? 30" class="w-full accent-[var(--brand)]" />
        </div>
        <details class="rounded-xl border border-line-soft px-3.5 py-2.5 sm:col-span-2">
          <summary class="cursor-pointer text-[13px] font-medium">Advanced</summary>
          <div class="mt-3 grid gap-4 sm:grid-cols-2">
            <div>
              <label class="label" for="rs-hops">Default hop limit</label>
              <select id="rs-hops" v-model.number="form.hop_limit" class="input">
                <option v-for="n in 7" :key="n" :value="n">{{ n }} hop{{ n === 1 ? '' : 's' }}{{ n === 3 ? ' (Meshtastic default)' : '' }}</option>
              </select>
            </div>
            <div>
              <label class="label" for="rs-baud">Modem baud rate</label>
              <select id="rs-baud" v-model.number="form.baud" class="input">
                <option v-for="b in [115200, 230400, 460800, 921600]" :key="b" :value="b">{{ b }}</option>
              </select>
            </div>
            <div>
              <label class="label" for="rs-chnum">Channel number</label>
              <input id="rs-chnum" v-model.number="form.channel_num" type="number" min="0" class="input tabular-nums" />
              <p class="hint">0 = picked from the primary channel name.</p>
            </div>
            <div>
              <label class="label" for="rs-ovf">Frequency override (MHz)</label>
              <input id="rs-ovf" v-model.number="form.override_frequency_mhz" type="number" step="0.001" min="0" class="input tabular-nums" />
              <p class="hint" :class="form.override_frequency_mhz ? '!text-warn' : ''">0 = region and preset decide.</p>
            </div>
          </div>
        </details>
      </div>

      <aside class="rounded-xl border border-line-soft bg-raised p-4">
        <div class="eyebrow">Resulting PHY</div>
        <template v-if="phy">
          <div class="mt-2 text-[28px] font-semibold leading-none tracking-tight tabular-nums">{{ num(phy.frequency_mhz + (form.frequency_offset_mhz || 0), 3) }}<span class="ml-1 text-sm font-normal text-ink-3">MHz</span></div>
          <div class="mt-1 text-xs text-ink-3">slot {{ phy.slot + 1 }}/{{ phy.num_slots }} · "{{ phy.primary_channel }}"</div>
          <dl class="kv mt-4 !grid-cols-[auto_1fr] text-xs">
            <dt>Bandwidth</dt><dd class="tabular-nums">{{ num(phy.bw_khz) }} kHz</dd>
            <dt>Spreading factor</dt><dd>SF{{ phy.sf }}</dd>
            <dt>Coding rate</dt><dd>4/{{ phy.cr }}</dd>
            <dt>Sync word</dt><dd class="mono">0x{{ phy.sync_word.toString(16).toUpperCase() }}</dd>
            <dt>TX power</dt><dd>{{ phy.tx_power_dbm }} dBm</dd>
          </dl>
          <p v-if="freqChanged" class="mt-3 rounded-lg bg-warn/10 px-2.5 py-2 text-xs text-warn">
            This moves {{ summary?.name }} off {{ num(summary?.phy.frequency_mhz, 3) }} MHz. Its identities leave their current mesh.
          </p>
        </template>
        <div v-else class="mt-2 h-24 animate-pulse rounded-lg bg-sunken" />
      </aside>
    </div>
    <RadioNodesPanel v-if="radioId && form" :radio-id="radioId" :board="form.type === 'meshtastic'" />
    <p v-if="error" class="mt-3 text-[13px] text-bad">{{ error }}</p>
    <template #footer>
      <button type="button" class="btn" @click="emit('close')">Cancel</button>
      <button type="button" class="btn btn-primary" :disabled="saving || !form || !dirty" @click="save"><Spinner v-if="saving" />Save radio</button>
    </template>
  </Modal>
</template>
