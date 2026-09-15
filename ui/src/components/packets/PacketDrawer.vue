<script setup lang="ts">
import { computed, ref } from 'vue'
import { ArrowDownLeft, ArrowUpRight, Lock } from '@lucide/vue'
import type { Packet } from '@/api/types'
import Drawer from '@/components/ui/Drawer.vue'
import KindChip from '@/components/ui/KindChip.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import { decodeHeader, hexDump } from '@/lib/header'
import { dateTime, hex, portLabel, BROADCAST } from '@/lib/format'
import { nodeByLastByte, nodeLabel } from '@/store/live'

const props = defineProps<{ packet: Packet | null }>()
const emit = defineEmits<{ close: [] }>()

const h = computed(() => (props.packet ? decodeHeader(props.packet.raw) : null))
const dump = computed(() => (props.packet ? hexDump(props.packet.raw) : []))
const hoverField = ref<number | null>(null)

const fieldColors = ['var(--s1)', 'var(--s2)', 'var(--s3)', 'var(--s7)', 'var(--s4)', 'var(--s5)', 'var(--s6)']
function fieldOf(byte: number) {
  return h.value?.fields.findIndex((f) => byte >= f.offset && byte < f.offset + f.length) ?? -1
}

const flagBits = computed(() => {
  const f = h.value?.flags
  if (!f) return []
  const bits = f.raw.toString(2).padStart(8, '0').split('')
  return [
    { label: 'hop_start', bits: bits.slice(0, 3), value: String(f.hopStart), span: 3 },
    { label: 'via_mqtt', bits: bits.slice(3, 4), value: f.viaMqtt ? 'yes' : 'no', span: 1 },
    { label: 'want_ack', bits: bits.slice(4, 5), value: f.wantAck ? 'yes' : 'no', span: 1 },
    { label: 'hop_limit', bits: bits.slice(5, 8), value: String(f.hopLimit), span: 3 },
  ]
})

const json = computed(() => (props.packet?.payload == null ? null : JSON.stringify(props.packet.payload, null, 2)))

function lastByteName(b: number) {
  if (!b) return 'none'
  const n = nodeByLastByte(b)
  return n ? `${n.short_name} (${n.long_name})` : 'unknown'
}
</script>

<template>
  <Drawer :open="!!packet" wide @close="emit('close')">
    <template #header>
      <div v-if="packet" class="flex flex-wrap items-center gap-2">
        <ArrowUpRight v-if="packet.direction === 'tx'" class="size-4 text-brand" />
        <ArrowDownLeft v-else class="size-4 text-info" />
        <h2 class="text-[15px] font-semibold tracking-tight">{{ portLabel(packet.port) }}</h2>
        <KindChip :kind="packet.kind" />
        <span v-if="packet.pki" class="chip bg-sunken text-ink-2"><Lock class="size-3" />PKI</span>
      </div>
      <p v-if="packet" class="mt-0.5 text-[13px] text-ink-3">
        <span class="mono">{{ hex(packet.id) }}</span> · seq {{ packet.seq }} · {{ dateTime(packet.time) }}
      </p>
    </template>

    <div v-if="packet" class="space-y-6">
      <p v-if="packet.summary" class="rounded-xl border border-line-soft bg-raised px-3.5 py-2.5 text-[13px]">{{ packet.summary }}</p>

      <dl class="kv">
        <dt>From</dt>
        <dd><span class="font-medium">{{ nodeLabel(packet.from).long }}</span> <span class="mono text-ink-3">{{ packet.from }}</span></dd>
        <dt>To</dt>
        <dd>
          <span class="font-medium">{{ packet.to === BROADCAST ? 'Broadcast' : nodeLabel(packet.to).long }}</span>
          <span class="mono text-ink-3"> {{ packet.to }}</span>
        </dd>
        <dt>Channel</dt>
        <dd>{{ packet.channel || 'unknown' }} <span class="mono text-ink-3">hash 0x{{ packet.channel_hash.toString(16).padStart(2, '0') }}</span></dd>
        <dt>Decrypted by</dt>
        <dd>
          <template v-if="packet.decoded_by">{{ nodeLabel(packet.decoded_by).long }} <span class="mono text-ink-3">{{ packet.decoded_by }}</span>
            <span class="text-ink-3"> · {{ packet.pki ? 'PKI key' : 'channel key' }}</span></template>
          <span v-else class="text-bad">no matching key</span>
        </dd>
        <dt>Signal</dt>
        <dd class="tabular-nums">
          <template v-if="packet.direction === 'rx'">SNR {{ packet.snr?.toFixed(2) }} dB · RSSI {{ packet.rssi }} dBm</template>
          <template v-else>transmitted</template>
        </dd>
        <dt>Size · airtime</dt>
        <dd class="tabular-nums">{{ packet.size }} bytes · {{ packet.airtime_ms }} ms</dd>
        <dt>Relay node</dt>
        <dd><span class="mono">0x{{ packet.relay_node.toString(16).padStart(2, '0') }}</span> <span class="text-ink-3">{{ lastByteName(packet.relay_node) }}</span></dd>
      </dl>

      <section v-if="h">
        <h3 class="eyebrow mb-2">Header · 16 bytes</h3>
        <div class="grid grid-cols-8 gap-1 sm:grid-cols-16">
          <div
            v-for="(b, i) in h.bytes.slice(0, 16)"
            :key="i"
            :class="['flex flex-col items-center rounded-md border py-1 transition-opacity', hoverField !== null && hoverField !== fieldOf(i) ? 'opacity-35' : '']"
            :style="{ borderColor: `color-mix(in srgb, ${fieldColors[fieldOf(i)]} 45%, transparent)`, background: `color-mix(in srgb, ${fieldColors[fieldOf(i)]} 10%, transparent)` }"
            @pointerenter="hoverField = fieldOf(i)"
            @pointerleave="hoverField = null"
          >
            <span class="mono text-[12px] font-semibold">{{ b.toString(16).padStart(2, '0') }}</span>
            <span class="text-[9px] tabular-nums text-ink-3">{{ i }}</span>
          </div>
        </div>
        <div class="mt-3 grid gap-1 sm:grid-cols-2">
          <div
            v-for="(f, i) in h.fields"
            :key="f.name"
            :class="['flex items-center gap-2 rounded-lg px-2 py-1 text-xs transition-colors', hoverField === i ? 'bg-sunken' : '']"
            @pointerenter="hoverField = i"
            @pointerleave="hoverField = null"
          >
            <span class="size-2 shrink-0 rounded-sm" :style="{ background: fieldColors[i] }" />
            <span class="text-ink-2">{{ f.name }}</span>
            <span class="text-2xs text-ink-3">[{{ f.offset }}{{ f.length > 1 ? `–${f.offset + f.length - 1}` : '' }}]</span>
            <span class="mono ml-auto">{{ f.value }}</span>
            <span v-if="f.hint" class="text-2xs text-ink-3">{{ f.hint }}</span>
          </div>
        </div>
      </section>

      <section v-if="h">
        <h3 class="eyebrow mb-2">Flags byte · 0x{{ h.flags.raw.toString(16).padStart(2, '0') }}</h3>
        <div class="flex gap-1">
          <div v-for="g in flagBits" :key="g.label" class="flex flex-col gap-1" :style="{ flex: g.span }">
            <div class="flex gap-1">
              <span
                v-for="(bit, k) in g.bits"
                :key="k"
                :class="['mono flex h-8 flex-1 items-center justify-center rounded-md border text-[13px] font-semibold', bit === '1' ? 'border-brand/40 bg-brand/12 text-brand' : 'border-line-soft bg-raised text-ink-3']"
              >{{ bit }}</span>
            </div>
            <div class="text-center text-2xs leading-tight text-ink-3">{{ g.label }}<br /><span class="font-medium text-ink">{{ g.value }}</span></div>
          </div>
        </div>
      </section>

      <section>
        <h3 class="eyebrow mb-2">Decoded payload</h3>
        <pre v-if="json" class="mono scroll-thin max-h-72 overflow-auto rounded-xl border border-line-soft bg-sunken p-3 text-[12px] leading-relaxed">{{ json }}</pre>
        <p v-else class="text-[13px] text-ink-3">Encrypted with a channel key none of our identities hold.</p>
      </section>

      <section>
        <div class="mb-2 flex items-center justify-between">
          <h3 class="eyebrow">Raw frame · {{ dump.length ? packet.raw.length / 2 : 0 }} bytes</h3>
          <CopyButton :text="packet.raw" label="Raw hex" />
        </div>
        <div class="mono scroll-thin overflow-x-auto rounded-xl border border-line-soft bg-sunken p-3 text-[12px] leading-relaxed">
          <div v-for="r in dump" :key="r.offset" class="flex gap-4 whitespace-pre">
            <span class="text-ink-3">{{ r.offset }}</span><span>{{ r.hex.padEnd(47, ' ') }}</span><span class="text-ink-3">{{ r.ascii }}</span>
          </div>
        </div>
      </section>
    </div>
  </Drawer>
</template>
