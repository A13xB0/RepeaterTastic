<script setup lang="ts">
// One meshtasticd instance: why it stopped, and what it printed (last 1000 lines, refreshed live).
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { Search } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { HostedInstance, HostedLogLine } from '@/api/types'
import Drawer from '@/components/ui/Drawer.vue'
import { clock, relTime } from '@/lib/format'
import { now } from '@/composables/now'

const props = defineProps<{ instance: HostedInstance | null; title: string }>()
const emit = defineEmits<{ close: [] }>()

const lines = ref<HostedLogLine[]>([])
const q = ref('')
const failed = ref('')
const follow = ref(true)
const box = ref<HTMLElement | null>(null)
let timer: number | undefined

async function load() {
  const x = props.instance
  if (!x) return
  try {
    lines.value = await api.get<HostedLogLine[]>(`/hosted/${enc(x.name)}/log`)
    failed.value = ''
  } catch (e) {
    failed.value = (e as Error).message
  }
}

watch(
  () => props.instance?.name,
  (name) => {
    clearInterval(timer)
    lines.value = []
    q.value = ''
    follow.value = true
    if (!name) return
    load()
    timer = window.setInterval(load, 3000)
  },
  { immediate: true },
)
onBeforeUnmount(() => clearInterval(timer))

const shown = computed(() => {
  const term = q.value.trim().toLowerCase()
  return term ? lines.value.filter((l) => l.text.toLowerCase().includes(term)) : lines.value
})
// meshtasticd starts each line with its level: "INFO  | 21:30:31 ..."
function levelCls(text: string) {
  if (/^(ERROR|CRIT)/.test(text)) return 'text-bad'
  if (/^WARN/.test(text)) return 'text-warn'
  if (/^DEBUG/.test(text)) return 'text-ink-3'
  return 'text-ink-2'
}
const stops = computed(() => [...(props.instance?.stops ?? [])].reverse())

function onScroll() {
  const el = box.value
  if (el) follow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}
watch(shown, async () => {
  if (!follow.value) return
  await nextTick()
  if (box.value) box.value.scrollTop = box.value.scrollHeight
})
</script>

<template>
  <Drawer :open="!!instance" :title="title" :subtitle="instance ? `${instance.name} · port ${instance.port} · ${instance.launcher}` : ''" wide @close="emit('close')">
    <template v-if="instance">
      <section aria-labelledby="mtd-stops">
        <h3 id="mtd-stops" class="eyebrow mb-1.5">Stops</h3>
        <p class="text-xs text-ink-3">
          {{ instance.running && instance.since ? `Running since ${relTime(instance.since, now)}.` : 'Not running.' }}
          {{ instance.restarts }} unexpected {{ instance.restarts === 1 ? 'stop' : 'stops' }}, {{ instance.reboots }} {{ instance.reboots === 1 ? 'reboot' : 'reboots' }} to apply settings.
        </p>
        <ul v-if="stops.length" class="mt-2 divide-y divide-line-soft rounded-xl border border-line-soft text-xs">
          <li v-for="s in stops" :key="s.time" class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 px-3 py-1.5">
            <span class="tabular-nums text-ink-3" :title="new Date(s.time).toLocaleString()">{{ relTime(s.time, now) }}</span>
            <span :class="['chip', s.reboot ? 'bg-info/12 text-info' : 'bg-bad/12 text-bad']">{{ s.reboot ? 'reboot' : 'stopped' }}</span>
            <span class="mono min-w-0 break-all text-ink-2">{{ s.reboot ? 'applied new settings' : s.reason }}</span>
          </li>
        </ul>
      </section>

      <section class="mt-5" aria-labelledby="mtd-log">
        <div class="mb-1.5 flex flex-wrap items-center justify-between gap-2">
          <h3 id="mtd-log" class="eyebrow">Output <span class="normal-case tracking-normal text-ink-3">· last {{ lines.length }} lines</span></h3>
          <div class="relative">
            <Search class="pointer-events-none absolute left-2.5 top-2 size-3.5 text-ink-3" />
            <input id="mtd-log-filter" v-model="q" class="input h-7 w-48 rounded-lg pl-8 text-xs" placeholder="Filter" aria-label="Filter the output" />
          </div>
        </div>
        <p v-if="failed" class="text-xs text-bad">{{ failed }}</p>
        <div ref="box" class="scroll-thin mono h-[60vh] overflow-auto rounded-xl border border-line-soft bg-sunken px-3 py-2 text-[11px] leading-relaxed" @scroll="onScroll">
          <div v-if="!shown.length" class="text-ink-3">{{ lines.length ? 'No lines match.' : 'Nothing printed yet.' }}</div>
          <div v-for="(l, i) in shown" :key="i" class="whitespace-pre-wrap break-all">
            <span class="mr-2 select-none text-ink-3">{{ clock(l.time) }}</span><span :class="levelCls(l.text)">{{ l.text }}</span>
          </div>
        </div>
      </section>
    </template>
  </Drawer>
</template>
