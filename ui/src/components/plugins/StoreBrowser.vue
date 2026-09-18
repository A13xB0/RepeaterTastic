<script setup lang="ts">
// The Browse store tab. The daemon reads the store and does the downloading, so this only asks it
// what's on offer and what's already installed here.
import { computed, onMounted, ref } from 'vue'
import { CloudOff, RefreshCw, Search, Store } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Plugin, StorePlugin, StoreResponse } from '@/api/types'
import { relTime } from '@/lib/format'
import { toast, toastError } from '@/composables/toast'
import StoreCard from './StoreCard.vue'

const props = defineProps<{ permissions: Record<string, string> }>()
const emit = defineEmits<{ installed: [plugin: Plugin] }>()

const data = ref<StoreResponse | null>(null)
const loading = ref(false)
const installing = ref('')
const query = ref('')
const updatesOnly = ref(false)

const shown = computed(() => {
  const list = data.value?.plugins ?? []
  const q = query.value.trim().toLowerCase()
  return list.filter((p) => {
    if (updatesOnly.value && !p.update_available) return false
    if (!q) return true
    return [p.name, p.summary, p.author, ...(p.tags ?? [])].some((s) => s?.toLowerCase().includes(q))
  })
})
const updates = computed(() => (data.value?.plugins ?? []).filter((p) => p.update_available).length)
const fetched = computed(() => (data.value?.fetched_at ? relTime(data.value.fetched_at * 1000) : ''))

async function load(refresh = false) {
  loading.value = true
  try {
    data.value = await api.get<StoreResponse>(`/plugins/store${refresh ? '?refresh=1' : ''}`)
  } catch (e) {
    toastError(e)
  } finally {
    loading.value = false
  }
}

async function install(p: StorePlugin) {
  installing.value = p.id
  try {
    const installed = await api.post<Plugin>(`/plugins/store/${enc(p.id)}/install`)
    toast(`${installed.name} ${installed.version ?? ''} ${p.installed ? 'updated' : 'installed'}`)
    emit('installed', installed)
    await load()
  } catch (e) {
    toastError(e)
  } finally {
    installing.value = ''
  }
}

defineExpose({ load })
onMounted(() => load())
</script>

<template>
  <div>
    <div v-if="data && !data.enabled" class="card empty">
      The plugin store is turned off. Set <span class="mono">plugins.store_url</span> in the config file to a store's index.json and restart.
    </div>

    <template v-else>
      <div class="mb-4 flex flex-wrap items-center gap-2">
        <label class="relative min-w-48 flex-1">
          <Search class="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-ink-3" />
          <input v-model="query" type="search" class="input w-full pl-9" placeholder="Search the store" aria-label="Search the store" />
        </label>
        <button v-if="updates" type="button" class="btn btn-sm" :class="updatesOnly && 'btn-primary'" @click="updatesOnly = !updatesOnly">
          {{ updates }} update{{ updates === 1 ? '' : 's' }}
        </button>
        <button type="button" class="btn btn-sm" :disabled="loading" @click="load(true)"><RefreshCw :class="['size-3.5', loading && 'animate-spin']" />Refresh</button>
      </div>

      <div v-if="data?.error" class="mb-4 flex items-start gap-2.5 rounded-xl bg-warn/10 px-4 py-3 text-[13px] text-warn">
        <CloudOff class="mt-0.5 size-4 shrink-0" />
        <div>
          The store couldn't be read, so this list may be out of date: <span class="mono">{{ data.error }}</span>
        </div>
      </div>

      <div v-if="!data" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <div v-for="n in 3" :key="n" class="card h-44 animate-pulse" />
      </div>

      <div v-else-if="!shown.length" class="card flex flex-col items-center px-6 py-12 text-center">
        <span class="flex size-12 items-center justify-center rounded-2xl bg-sunken text-ink-3"><Store class="size-6" /></span>
        <h3 class="mt-3 text-[15px] font-semibold">{{ query || updatesOnly ? 'Nothing matches' : 'The store is empty' }}</h3>
        <p class="mt-1 max-w-md text-[13px] text-ink-3">
          <template v-if="query || updatesOnly">Try a different search, or clear the filters.</template>
          <template v-else>Nothing is listed at <span class="mono">{{ data.url }}</span> yet.</template>
        </p>
      </div>

      <div v-else class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <StoreCard v-for="p in shown" :key="p.id" :plugin="p" :permissions="permissions" :busy="installing === p.id" @install="install" />
      </div>

      <p v-if="data?.url" class="mt-4 text-xs text-ink-3">
        From <span class="mono">{{ data.url }}</span><template v-if="fetched">, read {{ fetched }}</template>. Downloads are checked against the store's
        checksum, and a new plugin arrives switched off until you grant its permissions.
      </p>
    </template>
  </div>
</template>
