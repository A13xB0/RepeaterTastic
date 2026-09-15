<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, ExternalLink, KeyRound, RotateCw, Trash } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Plugin, PluginLogLine, PluginsResponse } from '@/api/types'
import Toggle from '@/components/ui/Toggle.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import Modal from '@/components/ui/Modal.vue'
import PluginLogo from '@/components/plugins/PluginLogo.vue'
import PluginStateChip from '@/components/plugins/PluginStateChip.vue'
import EnablePluginModal from '@/components/plugins/EnablePluginModal.vue'
import PluginSettingsForm from '@/components/plugins/PluginSettingsForm.vue'
import PluginPanel from '@/components/plugins/PluginPanel.vue'
import { transmits } from '@/components/plugins/PluginBits'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'
import { now } from '@/composables/now'
import { on } from '@/store/live'
import { dateTime, relTime } from '@/lib/format'

type Tab = 'overview' | 'settings' | 'panel' | 'logs'
const route = useRoute()
const router = useRouter()
const id = computed(() => route.params.id as string)
const plugin = ref<Plugin | null>(null)
const meta = ref<PluginsResponse | null>(null)
const missing = ref(false)
const enabling = ref<Plugin | null>(null)

const tabs = computed(() => {
  const t: { id: Tab; label: string }[] = [{ id: 'overview', label: 'Overview' }]
  if (plugin.value?.has_panel) t.unshift({ id: 'panel', label: 'Panel' })
  t.push({ id: 'settings', label: 'Settings' }, { id: 'logs', label: 'Log' })
  return t
})
const tab = computed<Tab>(() => {
  const want = route.params.tab as Tab | undefined
  if (want && tabs.value.some((t) => t.id === want)) return want
  return plugin.value?.has_panel && plugin.value.state === 'running' ? 'panel' : 'overview'
})
const setTab = (t: Tab) => router.replace({ name: 'plugin', params: { id: id.value, tab: t } })

async function load() {
  try {
    const [p, m] = await Promise.all([api.get<Plugin>(`/plugins/${enc(id.value)}`), meta.value ? Promise.resolve(meta.value) : api.get<PluginsResponse>('/plugins')])
    plugin.value = p
    meta.value = m
    missing.value = false
  } catch (e) {
    if ((e as { status?: number }).status === 404) missing.value = true
    else toastError(e)
  }
}
watch(id, load, { immediate: true })

const off = on('plugin', (p) => {
  if (p.id !== id.value) return
  if (p.deleted) missing.value = true
  else plugin.value = p
})

async function toggle(on: boolean) {
  if (!plugin.value) return
  if (on) {
    enabling.value = plugin.value
    return
  }
  try {
    plugin.value = await api.post<Plugin>(`/plugins/${enc(id.value)}/disable`)
    toast(`${plugin.value.name} disabled`)
  } catch (e) {
    toastError(e)
  }
}

async function restart() {
  try {
    plugin.value = await api.post<Plugin>(`/plugins/${enc(id.value)}/restart`)
    toast(`${plugin.value.name} restarting`)
  } catch (e) {
    toastError(e)
  }
}

async function remove() {
  const p = plugin.value
  if (!p) return
  const ok = await confirmDialog({
    title: `Remove ${p.name}?`,
    body: p.kind === 'attached' ? 'Its token stops working. The program itself keeps running wherever it runs.' : 'The plugin is stopped and deleted, with its data folder.',
    confirm: 'Remove plugin',
    danger: true,
  })
  if (!ok) return
  try {
    await api.del(`/plugins/${enc(p.id)}`)
    toast(`${p.name} removed`)
    router.push({ name: 'plugins' })
  } catch (e) {
    toastError(e)
  }
}

// Attached plugins: replace the token.
const newToken = ref<{ token: string; address: string } | null>(null)
async function regenerate() {
  if (!plugin.value) return
  const ok = await confirmDialog({ title: 'Make a new token?', body: 'The old token stops working at once and the plugin is disconnected.', confirm: 'New token', danger: true })
  if (!ok) return
  try {
    newToken.value = await api.post(`/plugins/${enc(id.value)}/token`)
  } catch (e) {
    toastError(e)
  }
}

// Log
const logs = ref<PluginLogLine[]>([])
const follow = ref(true)
const logBox = ref<HTMLElement | null>(null)
let logTimer: number | undefined
async function loadLogs() {
  try {
    logs.value = await api.get<PluginLogLine[]>(`/plugins/${enc(id.value)}/logs`)
    if (follow.value) requestAnimationFrame(() => logBox.value && (logBox.value.scrollTop = logBox.value.scrollHeight))
  } catch {
    /* shown on the next tick */
  }
}
watch(
  tab,
  (t) => {
    clearInterval(logTimer)
    if (t === 'logs') {
      loadLogs()
      logTimer = window.setInterval(loadLogs, 3000)
    }
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  off()
  clearInterval(logTimer)
})
const levelClass: Record<string, string> = { error: 'text-bad', warn: 'text-warn', debug: 'text-ink-3', info: '' }
const granted = computed(() => plugin.value?.permissions.filter((p) => p.granted) ?? [])
</script>

<template>
  <div>
    <RouterLink :to="{ name: 'plugins' }" class="mb-3 inline-flex items-center gap-1.5 text-[13px] text-ink-3 hover:text-ink"><ArrowLeft class="size-3.5" />Plugins</RouterLink>

    <div v-if="missing" class="card empty">This plugin isn't installed any more.</div>
    <div v-else-if="!plugin" class="card h-40 animate-pulse" />

    <template v-else>
      <div class="card mb-4 flex flex-wrap items-start gap-4 p-4 sm:p-5">
        <PluginLogo :plugin="plugin" size="lg" />
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="truncate text-lg font-semibold tracking-tight">{{ plugin.name }}</h2>
            <PluginStateChip :plugin="plugin" />
            <span v-if="plugin.pinned" class="chip bg-info/12 text-info">config file</span>
          </div>
          <div class="mt-0.5 flex flex-wrap items-center gap-x-2 text-xs text-ink-3">
            <span class="mono">{{ plugin.id }}</span>
            <span v-if="plugin.version">v{{ plugin.version }}</span>
            <span v-if="plugin.author">by {{ plugin.author }}</span>
            <span v-if="plugin.license">{{ plugin.license }}</span>
            <a v-if="plugin.homepage" :href="plugin.homepage" target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-1 hover:text-ink">
              Homepage<ExternalLink class="size-3" />
            </a>
          </div>
          <p v-if="plugin.description" class="mt-2 max-w-3xl text-[13px] text-ink-2">{{ plugin.description }}</p>
        </div>
        <div class="flex items-center gap-2">
          <button v-if="plugin.enabled" class="btn btn-sm" :disabled="plugin.state === 'needs_settings' || plugin.state === 'needs_review'" @click="restart">
            <RotateCw class="size-3.5" />{{ plugin.state === 'crashed' ? 'Try again' : 'Restart' }}
          </button>
          <button class="btn btn-sm" :disabled="plugin.pinned" :title="plugin.pinned ? 'Set in the config file' : undefined" @click="remove"><Trash class="size-3.5" />Remove</button>
          <Toggle :model-value="plugin.enabled" :disabled="plugin.pinned" :label="`Enable ${plugin.name}`" @update:model-value="toggle" />
        </div>
      </div>

      <div v-if="plugin.state === 'needs_review' || plugin.state === 'needs_settings' || plugin.state === 'crashed' || plugin.state === 'unsupported'"
        :class="['mb-4 flex flex-wrap items-center gap-3 rounded-xl px-4 py-3 text-[13px]', plugin.state === 'crashed' || plugin.state === 'unsupported' ? 'bg-bad/10 text-bad' : 'bg-warn/10 text-warn']">
        <span class="flex-1">{{ plugin.detail }}</span>
        <button v-if="plugin.state === 'needs_review'" class="btn btn-sm" @click="enabling = plugin">Review permissions</button>
        <button v-if="plugin.state === 'needs_settings'" class="btn btn-sm" @click="setTab('settings')">Open settings</button>
      </div>

      <section class="card overflow-hidden">
        <div class="tabs px-3 sm:px-4" role="tablist">
          <button v-for="t in tabs" :key="t.id" role="tab" :aria-selected="tab === t.id" @click="setTab(t.id)">{{ t.label }}</button>
        </div>
        <div class="p-4 sm:p-6">
          <!-- PANEL -->
          <template v-if="tab === 'panel'">
            <PluginPanel v-if="plugin.state === 'running'" :plugin="plugin" />
            <p v-else class="text-[13px] text-ink-3">The panel shows while the plugin is running.</p>
          </template>

          <!-- OVERVIEW -->
          <div v-else-if="tab === 'overview'" class="grid gap-8 lg:grid-cols-2">
            <div>
              <div class="eyebrow mb-2">Status</div>
              <p v-if="plugin.status?.summary" :class="['text-[14px] font-medium', plugin.status.state === 'error' ? 'text-bad' : plugin.status.state === 'warning' ? 'text-warn' : '']">
                {{ plugin.status.summary }}
              </p>
              <p v-else class="text-[13px] text-ink-3">{{ plugin.detail || (plugin.state === 'running' ? 'Running. The plugin has not reported a status.' : 'Not running.') }}</p>
              <dl v-if="plugin.status?.fields && Object.keys(plugin.status.fields).length" class="kv mt-3 text-[13px]">
                <template v-for="(v, k) in plugin.status.fields" :key="k"><dt>{{ k }}</dt><dd class="break-words">{{ v }}</dd></template>
              </dl>
              <dl class="kv mt-5 text-[13px]">
                <dt>Kind</dt><dd>{{ plugin.kind === 'attached' ? 'Attached: runs elsewhere and connects in' : 'Managed: RepeaterTastic runs it' }}</dd>
                <template v-if="plugin.connected_at"><dt>Connected</dt><dd :title="dateTime(plugin.connected_at)">{{ relTime(plugin.connected_at, now) }}</dd></template>
                <template v-if="plugin.kind === 'managed'"><dt>Restarts</dt><dd class="tabular-nums">{{ plugin.restarts }}</dd></template>
                <template v-if="plugin.dropped_events"><dt>Events dropped</dt><dd class="tabular-nums text-warn" title="The plugin read events too slowly">{{ plugin.dropped_events }}</dd></template>
                <dt>Installed</dt><dd :title="dateTime(plugin.installed_at)">{{ relTime(plugin.installed_at, now) }}<template v-if="plugin.source"> · {{ plugin.source }}</template></dd>
              </dl>
            </div>
            <div>
              <div class="eyebrow mb-2">Permissions</div>
              <p v-if="!plugin.permissions.length" class="text-[13px] text-ink-3">None.</p>
              <ul v-else class="grid gap-2">
                <li v-for="p in plugin.permissions" :key="p.key" class="flex items-start gap-2 text-[13px]">
                  <span :class="['mt-1.5 size-1.5 shrink-0 rounded-full', p.granted ? (transmits(p.key) ? 'bg-warn' : 'bg-ok') : 'bg-ink-3/40']" />
                  <span :class="p.granted ? '' : 'text-ink-3 line-through'">{{ p.text }}</span>
                </li>
              </ul>
              <button v-if="plugin.enabled && !plugin.pinned" class="btn btn-sm mt-3" @click="enabling = plugin">Change permissions</button>
              <p v-if="granted.some((p) => transmits(p.key))" class="hint mt-3">
                Sends are capped at {{ meta?.messages_per_hour }} messages and {{ meta?.traceroutes_per_hour }} traceroutes an hour.
              </p>
              <template v-if="plugin.network?.length">
                <div class="eyebrow mb-2 mt-6">Talks to</div>
                <div class="flex flex-wrap gap-1.5"><span v-for="h in plugin.network" :key="h" class="chip mono bg-sunken text-ink-2">{{ h }}</span></div>
              </template>
              <template v-if="plugin.kind === 'attached'">
                <div class="eyebrow mb-2 mt-6">Connection</div>
                <p class="text-[13px] text-ink-2">
                  Address <span class="mono">{{ meta?.attach_address || 'attaching is off (plugins.listen)' }}</span>
                </p>
                <button class="btn btn-sm mt-2" @click="regenerate"><KeyRound class="size-3.5" />New token</button>
              </template>
            </div>
          </div>

          <!-- SETTINGS -->
          <PluginSettingsForm v-else-if="tab === 'settings'" :plugin="plugin" :identities="meta?.identities" @saved="(p) => (plugin = p)" />

          <!-- LOG -->
          <div v-else-if="tab === 'logs'">
            <div class="mb-2 flex items-center justify-between text-xs text-ink-3">
              <span>Output, reports and what RepeaterTastic did with the plugin. The last 1000 lines, kept until a restart.</span>
              <label class="flex items-center gap-1.5"><input v-model="follow" type="checkbox" class="accent-[var(--brand)]" />Follow</label>
            </div>
            <div ref="logBox" class="mono h-[28rem] overflow-auto rounded-xl bg-raised p-3 text-xs leading-relaxed">
              <div v-if="!logs.length" class="text-ink-3">Nothing yet.</div>
              <div v-for="(l, i) in logs" :key="i" class="flex gap-3 whitespace-pre-wrap">
                <span class="shrink-0 text-ink-3 tabular-nums">{{ new Date(l.time).toLocaleTimeString() }}</span>
                <span class="w-12 shrink-0 text-ink-3">{{ l.source }}</span>
                <span :class="['min-w-0 break-words', levelClass[l.level]]">{{ l.message }}</span>
              </div>
            </div>
          </div>
        </div>
      </section>
    </template>

    <EnablePluginModal
      :plugin="enabling"
      :messages-per-hour="meta?.messages_per_hour"
      :traceroutes-per-hour="meta?.traceroutes_per_hour"
      @close="enabling = null"
      @done="(p) => { plugin = p; enabling = null }"
      @settings="setTab('settings'); enabling = null"
    />
    <Modal :open="!!newToken" title="New token" subtitle="Shown only now." size="md" @close="newToken = null">
      <div v-if="newToken" class="relative">
        <pre class="mono overflow-x-auto rounded-lg bg-raised px-3 py-2.5 pr-10 text-xs">RT_PLUGIN_TOKEN={{ newToken.token }}</pre>
        <span class="absolute right-2 top-2"><CopyButton :text="newToken.token" label="Token" /></span>
      </div>
      <template #footer><button class="btn btn-primary" @click="newToken = null">Done</button></template>
    </Modal>
  </div>
</template>
