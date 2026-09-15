<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Gauge, Link2, Plus, Puzzle, RefreshCw } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Plugin, PluginsResponse } from '@/api/types'
import Toggle from '@/components/ui/Toggle.vue'
import PluginLogo from '@/components/plugins/PluginLogo.vue'
import PluginStateChip from '@/components/plugins/PluginStateChip.vue'
import InstallPluginModal from '@/components/plugins/InstallPluginModal.vue'
import EnablePluginModal from '@/components/plugins/EnablePluginModal.vue'
import AttachPluginModal from '@/components/plugins/AttachPluginModal.vue'
import SendLimitsModal from '@/components/plugins/SendLimitsModal.vue'
import { toast, toastError } from '@/composables/toast'
import { on } from '@/store/live'

const router = useRouter()
const data = ref<PluginsResponse | null>(null)
const loading = ref(false)
const installOpen = ref(false)
const attachOpen = ref(false)
const limitsOpen = ref(false)
const enabling = ref<Plugin | null>(null)

async function load() {
  loading.value = true
  try {
    data.value = await api.get<PluginsResponse>('/plugins')
  } catch (e) {
    toastError(e)
  } finally {
    loading.value = false
  }
}

function upsert(p: Plugin) {
  if (!data.value) return
  const i = data.value.plugins.findIndex((x) => x.id === p.id)
  if (p.deleted) {
    if (i >= 0) data.value.plugins.splice(i, 1)
  } else if (i >= 0) data.value.plugins[i] = p
  else data.value.plugins.push(p)
}

async function toggle(p: Plugin, on: boolean) {
  if (on) {
    enabling.value = p
    return
  }
  try {
    upsert(await api.post<Plugin>(`/plugins/${enc(p.id)}/disable`))
    toast(`${p.name} disabled`)
  } catch (e) {
    toastError(e)
  }
}

function installed(p: Plugin) {
  installOpen.value = false
  upsert(p)
  toast(`${p.name} ${p.version ?? ''} installed`)
  router.push({ name: 'plugin', params: { id: p.id } })
}

const off = on('plugin', upsert)
onMounted(load)
onBeforeUnmount(off)
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Plugins</h2>
        <p class="page-sub">Programs that extend RepeaterTastic: uploaders, bots, dashboards. Each runs on its own and only sees what you allow.</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <button class="btn btn-sm" :disabled="loading" @click="load"><RefreshCw :class="['size-3.5', loading && 'animate-spin']" />Refresh</button>
        <button
          v-if="data?.enabled"
          class="btn btn-sm"
          :title="`Each plugin may send ${data.messages_per_hour} messages and ${data.traceroutes_per_hour} traceroutes an hour`"
          @click="limitsOpen = true"
        >
          <Gauge class="size-3.5" />Send limits
        </button>
        <button v-if="data?.enabled" class="btn btn-sm" @click="attachOpen = true"><Link2 class="size-3.5" />Attach</button>
        <button v-if="data?.enabled" class="btn btn-sm btn-primary" @click="installOpen = true"><Plus class="size-3.5" />Install plugin</button>
      </div>
    </div>

    <div v-if="!data" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <div v-for="n in 3" :key="n" class="card h-40 animate-pulse" />
    </div>

    <div v-else-if="!data.enabled" class="card empty">
      Plugins are turned off. Set <span class="mono">plugins.enabled: true</span> in the config file and restart.
    </div>

    <div v-else-if="!data.plugins.length" class="card flex flex-col items-center px-6 py-12 text-center">
      <span class="flex size-12 items-center justify-center rounded-2xl bg-sunken text-ink-3"><Puzzle class="size-6" /></span>
      <h3 class="mt-3 text-[15px] font-semibold">No plugins yet</h3>
      <p class="mt-1 max-w-md text-[13px] text-ink-3">
        Install a plugin bundle (.zip) from your computer or a URL, drop one into the plugins folder, or attach a plugin that runs elsewhere.
      </p>
      <div class="mt-4 flex gap-2">
        <button class="btn btn-primary" @click="installOpen = true"><Plus class="size-4" />Install plugin</button>
        <button class="btn" @click="attachOpen = true"><Link2 class="size-4" />Attach</button>
      </div>
    </div>

    <div v-else class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <section v-for="p in data.plugins" :key="p.id" class="card flex flex-col p-4 sm:p-5">
        <div class="flex items-start gap-3">
          <PluginLogo :plugin="p" />
          <div class="min-w-0 flex-1">
            <RouterLink :to="{ name: 'plugin', params: { id: p.id } }" class="block truncate text-[14px] font-semibold hover:underline">{{ p.name }}</RouterLink>
            <div class="truncate text-xs text-ink-3">
              {{ p.version ? `v${p.version}` : p.id }}<template v-if="p.author"> · {{ p.author }}</template><template v-if="p.kind === 'attached'"> · attached</template>
            </div>
          </div>
          <Toggle
            :model-value="p.enabled"
            :disabled="p.pinned"
            :label="`Enable ${p.name}`"
            :title="p.pinned ? 'Set in the config file' : undefined"
            @update:model-value="toggle(p, $event)"
          />
        </div>
        <p v-if="p.description" class="mt-3 line-clamp-2 text-xs leading-relaxed text-ink-2">{{ p.description }}</p>
        <div class="mt-3 flex flex-wrap items-center gap-2">
          <PluginStateChip :plugin="p" />
          <span v-if="p.pinned" class="chip bg-info/12 text-info">config file</span>
        </div>
        <p
          v-if="p.state === 'running' && p.status?.summary"
          :class="['mt-2 truncate text-xs', p.status.state === 'error' ? 'text-bad' : p.status.state === 'warning' ? 'text-warn' : 'text-ink-2']"
          :title="p.status.summary"
        >
          {{ p.status.summary }}
        </p>
        <p v-else-if="p.detail" class="mt-2 line-clamp-2 text-xs text-ink-3">{{ p.detail }}</p>
        <div class="mt-auto flex justify-end gap-2 pt-4">
          <RouterLink v-if="p.has_panel" class="btn btn-sm" :to="{ name: 'plugin', params: { id: p.id, tab: 'panel' } }">Open panel</RouterLink>
          <RouterLink class="btn btn-sm" :to="{ name: 'plugin', params: { id: p.id } }">Details</RouterLink>
        </div>
      </section>
    </div>

    <InstallPluginModal :open="installOpen" :allow-url="!!data?.allow_url_install" :folder="data?.folder" @close="installOpen = false" @installed="installed" />
    <SendLimitsModal
      :open="limitsOpen"
      :messages-per-hour="data?.messages_per_hour"
      :traceroutes-per-hour="data?.traceroutes_per_hour"
      @close="limitsOpen = false"
      @saved="(l) => { if (data) Object.assign(data, l); limitsOpen = false }"
    />
    <AttachPluginModal
      :open="attachOpen"
      :permissions="data?.permissions ?? {}"
      :address="data?.attach_address"
      @close="attachOpen = false"
      @attached="upsert"
    />
    <EnablePluginModal
      :plugin="enabling"
      :messages-per-hour="data?.messages_per_hour"
      :traceroutes-per-hour="data?.traceroutes_per_hour"
      @close="enabling = null"
      @done="(p) => { upsert(p); enabling = null }"
      @settings="router.push({ name: 'plugin', params: { id: enabling!.id, tab: 'settings' } }); enabling = null"
    />
  </div>
</template>
