<script setup lang="ts">
// A radio's MQTT broker connections: add, remove and edit each one. Bridge mode can carry
// private traffic off the mesh, so choosing it asks for confirmation first.
import { computed, ref } from 'vue'
import { ChevronDown, Plus, Trash, TriangleAlert } from '@lucide/vue'
import type { MqttConnection, MqttMode } from '@/api/types'
import { live } from '@/store/live'
import { confirmDialog } from '@/composables/confirm'

const list = defineModel<MqttConnection[]>({ required: true })
const open = ref<number | null>(list.value.length === 1 ? 0 : null)

const modes: { id: MqttMode; label: string; about: string }[] = [
  { id: 'gateway', label: 'Gateway', about: 'Uplink and downlink, like a Meshtastic node with MQTT on. Respects senders’ “OK to MQTT”; broker traffic stays off air unless relaying is on, and then only zero-hop.' },
  { id: 'uplink_only', label: 'Uplink only', about: 'Publishes what the mesh hears; never subscribes, so nothing from the broker reaches the radio.' },
  { id: 'map_only', label: 'Map only', about: 'Only the map report. No packets in either direction.' },
  { id: 'monitor', label: 'Monitor', about: 'Uplink for dashboards and loggers, JSON by default. JSON is plaintext, so it is only published for channels anyone can read.' },
  { id: 'bridge', label: 'Bridge', about: 'Joins your own sites or a private mesh over a broker you control. Can uplink packets from senders who did not opt in, publish private channels as JSON and rebroadcast further than zero-hop.' },
]
const selections = [
  { id: 'identity', label: 'Identity channel flags', about: 'Channels whose uplink/downlink switches are on for some identity, as set in the identity editor or the Meshtastic app.' },
  { id: 'override', label: 'Only the channels listed here', about: 'Ignores the identity switches: this connection carries exactly the channels ticked below.' },
  { id: 'combine', label: 'Listed here plus identity flags', about: 'The channels ticked below, and any channel an identity has switched on.' },
] as const

const channelNames = computed(() => {
  const names = new Set<string>()
  for (const id of live.identities) for (const ch of id.channels ?? []) if (ch.role !== 'DISABLED') names.add(ch.display_name || ch.name)
  return [...names].sort()
})
const gateways = computed(() => live.identities.map((i) => ({ value: i.is_relay ? 'relay' : i.node_id, label: `${i.long_name} · ${i.node_id}${i.is_relay ? ' (relay)' : ''}` })))

function blank(): MqttConnection {
  let n = list.value.length + 1
  while (list.value.some((c) => c.name === `mqtt-${n}`)) n++
  return {
    key: '', name: list.value.length ? `mqtt-${n}` : 'mqtt', enabled: false, address: '', username: '', password: '', password_set: false,
    clear_password: false, tls: false, root: '', mode: 'gateway', gateway: 'relay', format: 'encrypted', uplink_channels: [],
    downlink_channels: [], channel_selection: 'identity', ignore_consent: false, ok_to_mqtt: false, relay_mqtt: false, relay_hops: 0,
    cross_link: false, bridge_acknowledged: false, downlink_per_minute: 30, uplink_per_minute: 120,
    map_report: { enabled: false, interval: '1h', position_precision: 14, latitude: 0, longitude: 0 },
  }
}
function add() {
  list.value.push(blank())
  open.value = list.value.length - 1
}
async function remove(i: number) {
  const c = list.value[i]
  if (!(await confirmDialog({ title: `Remove “${c.name}”?`, body: 'The connection closes when the daemon restarts after you save.', confirm: 'Remove', danger: true }))) return
  list.value.splice(i, 1)
  open.value = null
}

async function setMode(c: MqttConnection, mode: MqttMode) {
  if (mode === c.mode) return
  if (mode === 'bridge') {
    const ok = await confirmDialog({
      title: 'Make this connection a bridge?',
      body: 'A bridge can publish packets from nodes that did not agree to MQTT, publish private channels as readable JSON, and put broker traffic on air more than one hop. Only use it with a broker you control. Are you sure?',
      confirm: 'Yes, make it a bridge',
      danger: true,
    })
    if (!ok) return
    c.bridge_acknowledged = true
  } else {
    c.bridge_acknowledged = false
    c.ignore_consent = false
    c.relay_hops = 0
  }
  if (mode === 'monitor' && c.format === 'encrypted') c.format = 'json'
  if (mode !== 'monitor' && c.mode === 'monitor' && c.format === 'json') c.format = 'encrypted'
  c.mode = mode
}

function toggleChannel(listed: string[], name: string) {
  const i = listed.indexOf(name)
  if (i >= 0) listed.splice(i, 1)
  else listed.push(name)
}

const uplinks = (c: MqttConnection) => c.mode !== 'map_only'
const downlinks = (c: MqttConnection) => c.mode === 'gateway' || c.mode === 'bridge'
const modeLabel = (m: MqttMode) => modes.find((x) => x.id === m)?.label ?? m
</script>

<template>
  <div class="max-w-3xl space-y-3">
    <p class="text-xs text-ink-3">
      Each connection is its own broker session with its own gateway identity and channels. Packets from one connection only reach another when both allow passing traffic on.
    </p>

    <div v-for="(c, i) in list" :key="i" class="rounded-xl border border-line-soft">
      <div class="flex items-center gap-3 px-3.5 py-3">
        <input :id="`mq-en-${i}`" v-model="c.enabled" type="checkbox" class="size-4 accent-[var(--brand)]" :aria-label="`Enable ${c.name}`" />
        <button type="button" class="flex min-w-0 flex-1 items-center gap-2 text-left" :aria-expanded="open === i" @click="open = open === i ? null : i">
          <span class="truncate text-[13px] font-medium">{{ c.name || 'unnamed' }}</span>
          <span :class="['chip', c.mode === 'bridge' ? 'text-warn' : '']">{{ modeLabel(c.mode) }}</span>
          <span class="mono truncate text-xs text-ink-3">{{ c.address || 'no broker' }}{{ c.root ? ` · ${c.root}` : '' }}</span>
          <ChevronDown :class="['ml-auto size-4 shrink-0 text-ink-3 transition-transform', open === i ? 'rotate-180' : '']" />
        </button>
        <button type="button" class="icon-btn" :aria-label="`Remove ${c.name}`" title="Remove" @click="remove(i)"><Trash class="size-4" /></button>
      </div>

      <div v-if="open === i" class="grid gap-4 border-t border-line-soft px-3.5 py-4 sm:grid-cols-2">
        <div>
          <label class="label" :for="`mq-name-${i}`">Name</label>
          <input :id="`mq-name-${i}`" v-model.trim="c.name" class="input mono" placeholder="public" />
        </div>
        <div>
          <label class="label" :for="`mq-mode-${i}`">Mode</label>
          <select :id="`mq-mode-${i}`" :value="c.mode" class="input" @change="setMode(c, ($event.target as HTMLSelectElement).value as MqttMode); ($event.target as HTMLSelectElement).value = c.mode">
            <option v-for="m in modes" :key="m.id" :value="m.id">{{ m.label }}</option>
          </select>
        </div>
        <p :class="['hint sm:col-span-2 !mt-[-0.5rem]', c.mode === 'bridge' ? '!text-warn' : '']">
          <TriangleAlert v-if="c.mode === 'bridge'" class="mr-1 inline size-3.5 align-[-2px]" />{{ modes.find((m) => m.id === c.mode)?.about }}
        </p>

        <div>
          <label class="label" :for="`mq-addr-${i}`">Broker (host:port)</label>
          <input :id="`mq-addr-${i}`" v-model="c.address" class="input mono" placeholder="mqtt.meshtastic.org:1883" />
        </div>
        <div>
          <label class="label" :for="`mq-root-${i}`">Root topic</label>
          <input :id="`mq-root-${i}`" v-model="c.root" class="input mono" placeholder="msh/EU_868/Scotland" />
          <p class="hint">Empty uses <span class="mono">msh/&lt;region&gt;</span>.</p>
        </div>
        <div>
          <label class="label" :for="`mq-user-${i}`">Username</label>
          <input :id="`mq-user-${i}`" v-model="c.username" class="input" autocomplete="off" />
        </div>
        <div>
          <label class="label" :for="`mq-pass-${i}`">Password</label>
          <input :id="`mq-pass-${i}`" v-model="c.password" type="password" class="input" autocomplete="new-password" :placeholder="c.password_set && !c.clear_password ? 'saved · leave empty to keep' : ''" />
          <label v-if="c.password_set" class="mt-1 flex items-center gap-2 text-xs text-ink-3"><input v-model="c.clear_password" type="checkbox" class="size-3.5 accent-[var(--brand)]" /> Remove the saved password</label>
        </div>
        <label class="flex items-center gap-2 text-[13px]"><input v-model="c.tls" type="checkbox" class="size-4 accent-[var(--brand)]" /> TLS</label>
        <div>
          <label class="label" :for="`mq-gw-${i}`">Gateway identity</label>
          <select :id="`mq-gw-${i}`" v-model="c.gateway" class="input">
            <option v-for="g in gateways" :key="g.value" :value="g.value">{{ g.label }}</option>
            <option v-if="!gateways.some((g) => g.value === c.gateway)" :value="c.gateway">{{ c.gateway }}</option>
          </select>
          <p class="hint">Appears in topics, envelopes and the map report.</p>
        </div>

        <template v-if="uplinks(c)">
          <div class="sm:col-span-2"><h4 class="eyebrow mb-1 mt-2">Channels</h4></div>
          <div class="sm:col-span-2">
            <label class="label" :for="`mq-sel-${i}`">Which channels</label>
            <select :id="`mq-sel-${i}`" v-model="c.channel_selection" class="input">
              <option v-for="s in selections" :key="s.id" :value="s.id">{{ s.label }}</option>
            </select>
            <p class="hint">{{ selections.find((s) => s.id === c.channel_selection)?.about }}</p>
          </div>
          <div v-if="c.channel_selection !== 'identity'" class="grid gap-3 sm:col-span-2 sm:grid-cols-2">
            <fieldset>
              <legend class="label">Uplink</legend>
              <label v-for="ch in channelNames" :key="ch" class="flex items-center gap-2 py-0.5 text-[13px]">
                <input type="checkbox" class="size-4 accent-[var(--brand)]" :checked="c.uplink_channels.includes(ch)" @change="toggleChannel(c.uplink_channels, ch)" /> {{ ch }}
              </label>
            </fieldset>
            <fieldset v-if="downlinks(c)">
              <legend class="label">Downlink</legend>
              <label v-for="ch in channelNames" :key="ch" class="flex items-center gap-2 py-0.5 text-[13px]">
                <input type="checkbox" class="size-4 accent-[var(--brand)]" :checked="c.downlink_channels.includes(ch)" @change="toggleChannel(c.downlink_channels, ch)" /> {{ ch }}
              </label>
            </fieldset>
          </div>
          <div>
            <label class="label" :for="`mq-fmt-${i}`">Payload format</label>
            <select :id="`mq-fmt-${i}`" v-model="c.format" class="input">
              <option value="encrypted">Encrypted (ServiceEnvelope)</option>
              <option value="json">JSON</option>
              <option value="both">Both</option>
            </select>
            <p class="hint">JSON is readable text on <span class="mono">…/2/json/</span>{{ c.mode === 'bridge' ? '.' : ', only for channels anyone can read.' }}</p>
          </div>
          <div>
            <label class="label" :for="`mq-up-${i}`">Uplink limit · {{ c.uplink_per_minute }}/min</label>
            <input :id="`mq-up-${i}`" v-model.number="c.uplink_per_minute" type="range" min="5" max="600" step="5" class="w-full accent-[var(--brand)]" />
          </div>
        </template>
        <div v-if="downlinks(c)">
          <label class="label" :for="`mq-down-${i}`">Downlink limit · {{ c.downlink_per_minute }}/min</label>
          <input :id="`mq-down-${i}`" v-model.number="c.downlink_per_minute" type="range" min="1" max="120" step="1" class="w-full accent-[var(--brand)]" />
        </div>

        <div class="sm:col-span-2"><h4 class="eyebrow mb-1 mt-2">Behaviour</h4></div>
        <label class="flex items-start gap-2 text-[13px] sm:col-span-2">
          <input v-model="c.ok_to_mqtt" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" />
          <span>OK to MQTT<span class="block text-xs text-ink-3">Other gateways may uplink the packets our identities send. Any connection with this on sets it for the whole radio.</span></span>
        </label>
        <label v-if="downlinks(c)" class="flex items-start gap-2 text-[13px] sm:col-span-2">
          <input v-model="c.relay_mqtt" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" />
          <span :class="c.relay_mqtt ? 'text-warn' : ''">Relay this connection's traffic on air<span class="block text-xs text-ink-3">Off keeps its broker traffic off the radio (the firmware's “Ignore MQTT”). On, the relay persona rebroadcasts it{{ c.mode === 'bridge' ? '' : ' zero-hop' }}. Leave off on a busy site.</span></span>
        </label>
        <div v-if="c.mode === 'bridge' && c.relay_mqtt">
          <label class="label" :for="`mq-hops-${i}`">Hops after rebroadcast · {{ c.relay_hops }}</label>
          <input :id="`mq-hops-${i}`" v-model.number="c.relay_hops" type="range" min="0" max="3" step="1" class="w-full accent-[var(--brand)]" />
        </div>
        <label v-if="c.mode === 'bridge'" class="flex items-start gap-2 text-[13px] sm:col-span-2">
          <input v-model="c.ignore_consent" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" />
          <span class="text-warn">Uplink without consent<span class="block text-xs text-ink-3">Also publishes packets from senders who did not turn on “OK to MQTT”.</span></span>
        </label>
        <label v-if="c.mode !== 'map_only'" class="flex items-start gap-2 text-[13px] sm:col-span-2">
          <input v-model="c.cross_link" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" />
          <span>Pass traffic between connections<span class="block text-xs text-ink-3">Packets from this connection may go out on other connections that also allow it, and theirs on this one.</span></span>
        </label>

        <div class="sm:col-span-2"><h4 class="eyebrow mb-1 mt-2">Map report</h4></div>
        <label class="flex items-center gap-2 text-[13px]"><input v-model="c.map_report.enabled" type="checkbox" class="size-4 accent-[var(--brand)]" /> Publish to the map topic</label>
        <div>
          <label class="label" :for="`mq-mint-${i}`">Interval</label>
          <select :id="`mq-mint-${i}`" v-model="c.map_report.interval" class="input">
            <option v-for="v in ['15m', '30m', '1h', '3h', '6h']" :key="v" :value="v">every {{ v }}</option>
          </select>
        </div>
        <div class="sm:col-span-2">
          <label class="label" :for="`mq-mbits-${i}`">Map precision · {{ c.map_report.position_precision === 32 ? 'exact' : `${c.map_report.position_precision} bits` }}</label>
          <input :id="`mq-mbits-${i}`" v-model.number="c.map_report.position_precision" type="range" min="10" max="32" step="1" class="w-full accent-[var(--brand)]" />
          <p class="hint">Uses the site position from Position &amp; hardware. Public brokers coarsen positions whatever you send.</p>
        </div>
      </div>
    </div>

    <p v-if="!list.length" class="rounded-xl border border-dashed border-line-soft px-3.5 py-6 text-center text-[13px] text-ink-3">No MQTT connections. Broker traffic never touches this radio.</p>
    <div class="flex items-center justify-between gap-3">
      <button type="button" class="btn" @click="add"><Plus class="size-4" />Add connection</button>
      <p class="hint">Connection changes take effect after a restart.</p>
    </div>
  </div>
</template>
