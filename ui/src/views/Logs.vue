<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowDownToLine, Pause, Play, Search } from '@lucide/vue'
import type { LogLevel, LogLine } from '@/api/types'
import { logs, refreshLogs } from '@/store/live'
import { clock } from '@/lib/format'

const LEVELS: LogLevel[] = ['debug', 'info', 'warn', 'error']
const enabled = ref<Record<LogLevel, boolean>>({ debug: false, info: true, warn: true, error: true })
const q = ref('')
const paused = ref(false)
const frozen = ref<LogLine[]>([])
const follow = ref(true)
const box = ref<HTMLElement | null>(null)

const source = computed(() => (paused.value ? frozen.value : logs.value))
const counts = computed(() => {
  const c: Record<LogLevel, number> = { debug: 0, info: 0, warn: 0, error: 0 }
  for (const l of source.value) c[l.level]++
  return c
})
const shown = computed(() => {
  const term = q.value.trim().toLowerCase()
  const out = source.value.filter((l) => enabled.value[l.level] && (!term || l.msg.toLowerCase().includes(term)))
  return out.slice(-1000)
})

function togglePause() {
  paused.value = !paused.value
  if (paused.value) frozen.value = [...logs.value]
}

async function toEnd() {
  await nextTick()
  if (box.value) box.value.scrollTop = box.value.scrollHeight
}
function onScroll() {
  const el = box.value
  if (!el) return
  follow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}
watch(shown, () => follow.value && toEnd())
onMounted(async () => {
  if (!logs.value.length) await refreshLogs().catch(() => {})
  toEnd()
})
onBeforeUnmount(() => (paused.value = false))

const levelCls: Record<LogLevel, string> = {
  debug: 'text-ink-3',
  info: 'text-info',
  warn: 'text-warn',
  error: 'text-bad',
}
const chipCls: Record<LogLevel, string> = {
  debug: 'bg-ink-3/14 text-ink-2',
  info: 'bg-info/12 text-info',
  warn: 'bg-warn/15 text-warn',
  error: 'bg-bad/12 text-bad',
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Logs</h2>
        <p class="page-sub">Live tail of the daemon log · {{ shown.length }} of {{ source.length }} lines</p>
      </div>
    </div>

    <section class="card flex h-[calc(100dvh-1.5rem)] min-h-[420px] flex-col overflow-hidden sm:h-[calc(100dvh-13rem)] lg:h-[calc(100dvh-10.5rem)]">
      <div class="flex flex-wrap items-center gap-2 border-b border-line-soft p-3">
        <div class="flex flex-wrap gap-1">
          <button
            v-for="l in LEVELS"
            type="button"
            :key="l"
            :aria-pressed="enabled[l]"
            :class="['chip h-7 cursor-pointer rounded-lg px-2.5 text-xs transition-opacity', chipCls[l], enabled[l] ? '' : 'opacity-40 grayscale']"
            @click="enabled[l] = !enabled[l]"
          >
            {{ l }} <span class="tabular-nums opacity-70">{{ counts[l] }}</span>
          </button>
        </div>
        <div class="relative min-w-40 flex-1 sm:max-w-72">
          <Search class="pointer-events-none absolute left-2.5 top-2 size-3.5 text-ink-3" />
          <input id="logs-filter" v-model="q" aria-label="Filter messages" class="input h-7 rounded-lg pl-8 text-xs" placeholder="Filter messages" />
        </div>
        <div class="ml-auto flex gap-1.5">
          <button type="button" class="btn btn-sm" @click="togglePause"><Play v-if="paused" class="size-3.5" /><Pause v-else class="size-3.5" />{{ paused ? 'Resume' : 'Pause' }}</button>
          <button type="button" v-if="!follow" class="btn btn-sm" @click="follow = true; toEnd()"><ArrowDownToLine class="size-3.5" />Follow</button>
        </div>
      </div>
      <div ref="box" class="mono scroll-thin min-h-0 flex-1 overflow-auto bg-sunken/50 py-2 text-[12px] leading-[1.65]" @scroll="onScroll">
        <div v-for="(l, i) in shown" :key="`${l.time}-${i}`" class="flex gap-3 px-3 hover:bg-surface-solid/60 sm:px-4">
          <span class="shrink-0 tabular-nums text-ink-3">{{ clock(l.time) }}</span>
          <span :class="['w-10 shrink-0 font-semibold uppercase', levelCls[l.level]]">{{ l.level }}</span>
          <span :class="['min-w-0 whitespace-pre-wrap break-words', l.level === 'error' ? 'text-bad' : l.level === 'debug' ? 'text-ink-2' : 'text-ink']">{{ l.msg }}</span>
        </div>
        <div v-if="!shown.length" class="empty">No log lines match.</div>
      </div>
    </section>
  </div>
</template>
