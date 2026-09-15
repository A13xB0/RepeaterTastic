<script setup lang="ts">
// Browser chat for any identity: channel conversations and PKI DMs, live over SSE.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Check, CheckCheck, CircleAlert, Clock3, Hash, Lock, MessageCirclePlus, Search, Send } from '@lucide/vue'
import { api, enc, qs, radio as currentRadio, setRadio } from '@/api/client'
import type { Conversation, Message } from '@/api/types'
import { live, nodeLabel, on, refreshAllIdentities } from '@/store/live'
import NodeAvatar from '@/components/ui/NodeAvatar.vue'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { toastError } from '@/composables/toast'
import { now } from '@/composables/now'
import { BROADCAST, clock, dayLabel, relTime, utf8Len } from '@/lib/format'
import { sendError } from '@/lib/relay'

const route = useRoute()
const router = useRouter()

// Every identity can chat, the relay persona too (e.g. to DM a service that verifies the node);
// ordinary identities come first.
const chatIdentities = computed(() => [...live.identities].sort((a, b) => Number(a.is_relay) - Number(b.is_relay)))
const identityId = computed(() => {
  const p = route.params.identity as string | undefined
  return p || chatIdentities.value.find((i) => i.enabled && !i.is_relay)?.node_id || chatIdentities.value[0]?.node_id || ''
})
const identity = computed(() => live.identities.find((i) => i.node_id === identityId.value))
const convKey = computed(() => (route.params.conversation as string | undefined) || '')

// A chat link for an identity on another radio (or one that has just moved) switches the page to
// that radio instead of showing an empty chat.
watch(
  () => [identityId.value, live.identities.length, live.radios.length] as const,
  async ([id, loaded, radios]) => {
    if (!id || !loaded || radios < 2 || live.identities.some((i) => i.node_id === id)) return
    await refreshAllIdentities()
    const there = live.allIdentities.find((i) => i.node_id === id)
    if (there?.radio_id && there.radio_id !== currentRadio.value) setRadio(there.radio_id)
  },
  { immediate: true },
)

const conversations = ref<Conversation[]>([])
const loadingConvs = ref(false)
const messages = ref<Message[]>([])
const loadingMsgs = ref(false)
const hasOlder = ref(false)
const text = ref('')
const sending = ref(false)
const scroller = ref<HTMLElement | null>(null)
const composer = ref<HTMLTextAreaElement | null>(null)
const newDm = ref(false)
const dmSearch = ref('')

function selectIdentity(id: string) {
  router.push({ name: 'chat', params: { identity: id } })
}
function openConv(key: string) {
  router.push({ name: 'chat', params: { identity: identityId.value, conversation: key } })
}

async function loadConversations() {
  if (!identityId.value) return
  loadingConvs.value = !conversations.value.length
  try {
    conversations.value = await api.get<Conversation[]>(`/identities/${enc(identityId.value)}/conversations`)
    if (convKey.value) markRead()
  } catch (e) {
    toastError(e)
  } finally {
    loadingConvs.value = false
  }
}

const PAGE = 50
async function loadMessages(older = false) {
  if (!identityId.value || !convKey.value) return
  loadingMsgs.value = true
  const before = older && messages.value[0] ? messages.value[0].time : undefined
  try {
    const page = await api.get<Message[]>(`/identities/${enc(identityId.value)}/messages${qs({ conversation: convKey.value, before, limit: PAGE })}`)
    hasOlder.value = page.length === PAGE
    if (older) {
      const el = scroller.value
      const h = el?.scrollHeight ?? 0
      messages.value = [...page, ...messages.value]
      await nextTick()
      if (el) el.scrollTop = el.scrollHeight - h
    } else {
      messages.value = page
      await scrollToEnd()
    }
  } catch (e) {
    toastError(e)
  } finally {
    loadingMsgs.value = false
  }
}

async function markRead() {
  const c = conversations.value.find((x) => x.key === convKey.value)
  if (!c || !c.unread || !identity.value) return
  const n = c.unread
  c.unread = 0
  identity.value.unread = Math.max(0, (identity.value.unread ?? 0) - n)
  try {
    await api.post(`/identities/${enc(identityId.value)}/conversations/${enc(convKey.value)}/read`)
  } catch {
    /* non-fatal */
  }
}

async function scrollToEnd() {
  await nextTick()
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

watch(identityId, () => {
  conversations.value = []
  loadConversations()
}, { immediate: true })
watch([identityId, convKey], () => {
  messages.value = []
  text.value = ''
  loadMessages().then(markRead)
  if (convKey.value) nextTick(() => composer.value?.focus())
}, { immediate: true })

function keyFor(m: Message) {
  if (m.to === BROADCAST) return `ch:${m.channel}`
  return `dm:${m.direction === 'out' ? m.to : m.from}`
}

let convTimer: number | undefined
const offMsg = on('message', ({ identity: id, message }) => {
  if (id !== identityId.value) return
  if (keyFor(message) === convKey.value) {
    const idx = messages.value.findIndex((m) => m.id === message.id && m.direction === message.direction)
    if (idx >= 0) messages.value.splice(idx, 1, message)
    else {
      const el = scroller.value
      const atEnd = !el || el.scrollHeight - el.scrollTop - el.clientHeight < 120
      messages.value.push(message)
      if (atEnd || message.direction === 'out') scrollToEnd()
      if (message.direction === 'in' && document.visibilityState === 'visible')
        api.post(`/identities/${enc(id)}/conversations/${enc(convKey.value)}/read`).catch(() => {})
    }
  }
  clearTimeout(convTimer)
  convTimer = window.setTimeout(async () => {
    await loadConversations()
    markRead()
  }, 400)
})
onMounted(() => loadConversations())
onBeforeUnmount(() => {
  offMsg()
  clearTimeout(convTimer)
})

const current = computed(() => {
  const k = convKey.value
  if (!k) return null
  const conv = conversations.value.find((c) => c.key === k)
  if (k.startsWith('ch:')) {
    const idx = Number(k.slice(3))
    const ch = identity.value?.channels[idx]
    return { kind: 'channel' as const, title: conv?.title ?? ch?.display_name ?? `Channel ${idx}`, channel: idx, to: BROADCAST, sub: ch ? `channel ${idx} · ${ch.role.toLowerCase()} · ${ch.psk === 'AQ==' ? 'default key' : 'private key'}` : '' }
  }
  const nodeId = k.slice(3)
  const n = live.nodes[nodeId]
  return {
    kind: 'dm' as const,
    title: n?.long_name ?? conv?.title ?? nodeId,
    channel: 0,
    to: nodeId,
    nodeId,
    sub: n ? `${nodeId} · ${n.local ? 'local identity, delivered without RF' : n.has_public_key ? `PKI encrypted · heard ${relTime(n.last_heard, now.value)}` : 'no public key yet, will use the channel key'}` : nodeId,
  }
})

type Row = { type: 'day'; label: string; key: string } | { type: 'msg'; m: Message; first: boolean; key: string }
const rows = computed<Row[]>(() => {
  const out: Row[] = []
  let lastDay = ''
  let lastFrom = ''
  let lastTime = 0
  for (const m of messages.value) {
    const d = new Date(m.time).toDateString()
    if (d !== lastDay) {
      out.push({ type: 'day', label: dayLabel(m.time), key: `d${d}` })
      lastDay = d
      lastFrom = ''
    }
    const first = m.from !== lastFrom || m.time - lastTime > 10 * 60_000
    out.push({ type: 'msg', m, first, key: `${m.direction}${m.id}` })
    lastFrom = m.from
    lastTime = m.time
  }
  return out
})

const bytes = computed(() => utf8Len(text.value))
const canSend = computed(() => !!text.value.trim() && bytes.value <= 200 && !sending.value && !!identity.value?.enabled)

async function send() {
  if (!canSend.value || !current.value) return
  sending.value = true
  const body = { to: current.value.to, channel: current.value.channel, text: text.value.trim(), want_ack: true }
  try {
    const m = await api.post<Message>(`/identities/${enc(identityId.value)}/messages`, body)
    if (!messages.value.some((x) => x.id === m.id && x.direction === 'out')) messages.value.push(m)
    text.value = ''
    scrollToEnd()
  } catch (e) {
    toastError(e)
  } finally {
    sending.value = false
    nextTick(() => composer.value?.focus())
  }
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    send()
  }
}

const dmCandidates = computed(() => {
  const q = dmSearch.value.trim().toLowerCase()
  return Object.values(live.nodes)
    .filter((n) => n.node_id !== identityId.value)
    .filter((n) => !q || n.long_name.toLowerCase().includes(q) || n.short_name.toLowerCase().includes(q) || n.node_id.includes(q))
    .sort((a, b) => Number(b.local) - Number(a.local) || b.last_heard - a.last_heard)
    .slice(0, 60)
})
function startDm(id: string) {
  newDm.value = false
  dmSearch.value = ''
  openConv(`dm:${id}`)
}

const convIcon = (c: Conversation) => (c.key.startsWith('ch:') ? 'channel' : 'dm')
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Chat</h2>
        <p class="page-sub">Talk as any identity, straight from the browser.</p>
      </div>
    </div>

    <div class="card flex h-[calc(100dvh-1.5rem)] min-h-[420px] overflow-hidden sm:h-[calc(100dvh-13rem)] lg:h-[calc(100dvh-10.5rem)]">
      <!-- conversations -->
      <aside :class="['flex w-full shrink-0 flex-col border-line-soft md:w-80 md:border-r', convKey ? 'max-md:hidden' : '']">
        <div class="border-b border-line-soft p-3">
          <label class="label" for="chat-ident">Speaking as</label>
          <div class="flex gap-2">
            <select id="chat-ident" class="input" :value="identityId" @change="selectIdentity(($event.target as HTMLSelectElement).value)">
              <option v-for="i in chatIdentities" :key="i.node_id" :value="i.node_id">
                {{ i.long_name }} ({{ i.short_name }}){{ i.is_relay ? ' · relay persona' : '' }}{{ i.enabled ? '' : ' · disabled' }}{{ i.unread ? ` · ${i.unread} unread` : '' }}
              </option>
            </select>
            <button class="btn shrink-0 px-2.5" title="New direct message" @click="newDm = true"><MessageCirclePlus class="size-4" /></button>
          </div>
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto p-1.5">
          <div v-if="loadingConvs" class="space-y-1.5 p-1.5">
            <div v-for="n in 5" :key="n" class="h-14 animate-pulse rounded-xl bg-sunken" />
          </div>
          <button
            v-for="c in conversations"
            :key="c.key"
            :class="['flex w-full items-center gap-3 rounded-xl px-2.5 py-2 text-left transition-colors', c.key === convKey ? 'bg-brand/10' : 'hover:bg-sunken']"
            @click="openConv(c.key)"
          >
            <span v-if="convIcon(c) === 'channel'" class="flex h-8 min-w-11 items-center justify-center rounded-lg bg-brand/12 text-brand">
              <Hash class="size-4" />
            </span>
            <NodeAvatar v-else :id="c.key.slice(3)" :short="nodeLabel(c.key.slice(3)).short" :local="nodeLabel(c.key.slice(3)).local" />
            <span class="min-w-0 flex-1">
              <span class="flex items-baseline justify-between gap-2">
                <span :class="['truncate text-[13px]', c.unread ? 'font-semibold' : 'font-medium']">{{ c.title }}</span>
                <span class="shrink-0 text-2xs tabular-nums text-ink-3">{{ c.last_time ? relTime(c.last_time, now).replace(' ago', '') : '' }}</span>
              </span>
              <span class="flex items-center justify-between gap-2">
                <span :class="['truncate text-xs', c.unread ? 'text-ink-2' : 'text-ink-3']">{{ c.last_text || 'No messages yet' }}</span>
                <span v-if="c.unread" class="badge">{{ c.unread }}</span>
              </span>
            </span>
          </button>
          <p v-if="!loadingConvs && !conversations.length" class="empty">No conversations for this identity.</p>
        </div>
      </aside>

      <!-- thread -->
      <section :class="['flex min-w-0 flex-1 flex-col', !convKey ? 'max-md:hidden' : '']">
        <template v-if="current">
          <header class="flex items-center gap-3 border-b border-line-soft px-3 py-2.5 sm:px-4">
            <button class="icon-btn md:hidden" aria-label="Back to conversations" @click="router.push({ name: 'chat', params: { identity: identityId } })">
              <ArrowLeft class="size-4" />
            </button>
            <span v-if="current.kind === 'channel'" class="flex h-8 min-w-11 items-center justify-center rounded-lg bg-brand/12 text-brand"><Hash class="size-4" /></span>
            <NodeAvatar v-else :id="current.nodeId" :short="nodeLabel(current.nodeId).short" :local="nodeLabel(current.nodeId).local" />
            <div class="min-w-0">
              <div class="truncate text-[14px] font-semibold">{{ current.title }}</div>
              <div class="truncate text-xs text-ink-3">{{ current.sub }}</div>
            </div>
          </header>

          <div ref="scroller" class="min-h-0 flex-1 overflow-y-auto px-3 py-3 sm:px-5">
            <div v-if="hasOlder" class="mb-3 flex justify-center">
              <button class="btn btn-sm" :disabled="loadingMsgs" @click="loadMessages(true)"><Spinner v-if="loadingMsgs" />Load older</button>
            </div>
            <div v-if="loadingMsgs && !messages.length" class="flex justify-center py-10 text-ink-3"><Spinner /></div>
            <div v-else-if="!messages.length" class="empty h-full">
              <span class="text-[13px]">No messages yet. Say hello to {{ current.title }}.</span>
            </div>
            <template v-for="r in rows" :key="r.key">
              <div v-if="r.type === 'day'" class="my-3 flex items-center gap-3 text-2xs font-medium text-ink-3">
                <span class="h-px flex-1 bg-line-soft" />{{ r.label }}<span class="h-px flex-1 bg-line-soft" />
              </div>
              <div v-else :class="['flex gap-2', r.m.direction === 'out' ? 'justify-end' : 'justify-start', r.first ? 'mt-3' : 'mt-0.5']">
                <div v-if="r.m.direction === 'in' && current.kind === 'channel'" class="w-11 shrink-0">
                  <NodeAvatar v-if="r.first" :id="r.m.from" :short="nodeLabel(r.m.from).short" size="sm" :local="nodeLabel(r.m.from).local" />
                </div>
                <div :class="['flex max-w-[min(34rem,82%)] flex-col', r.m.direction === 'out' ? 'items-end' : 'items-start']">
                  <div v-if="r.first && r.m.direction === 'in' && current.kind === 'channel'" class="mb-0.5 px-1 text-2xs font-medium text-ink-2">
                    {{ nodeLabel(r.m.from).long }}
                  </div>
                  <div
                    :class="[
                      'whitespace-pre-wrap break-words rounded-2xl px-3.5 py-2 text-[13.5px] leading-snug',
                      r.m.direction === 'out'
                        ? r.m.status === 'failed' ? 'rounded-br-md border border-bad/40 bg-bad/10 text-ink' : 'rounded-br-md bg-brand text-brand-ink'
                        : 'rounded-bl-md border border-line-soft bg-raised text-ink',
                    ]"
                  >
                    {{ r.m.text }}
                  </div>
                  <div class="mt-0.5 flex items-center gap-1.5 px-1 text-2xs tabular-nums text-ink-3">
                    <Lock v-if="r.m.pki" class="size-2.5" />
                    <span>{{ clock(r.m.time, false) }}</span>
                    <span v-if="r.m.radio" :title="`${r.m.direction === 'in' ? 'Heard' : 'Sent'} on ${r.m.radio}`">· via {{ r.m.radio.split(', ').map((x) => live.radios.find((rr) => rr.id === x)?.name ?? x).join(' + ') }}</span>
                    <template v-if="r.m.direction === 'in' && r.m.snr != null">
                      <span>· SNR {{ r.m.snr.toFixed(1) }}</span><span v-if="r.m.hops != null">· {{ r.m.hops }} hop{{ r.m.hops === 1 ? '' : 's' }}</span>
                    </template>
                    <template v-if="r.m.direction === 'out'">
                      <Clock3 v-if="r.m.status === 'queued'" class="size-3" aria-label="queued" />
                      <Check v-else-if="r.m.status === 'sent'" class="size-3" aria-label="sent" />
                      <CheckCheck v-else-if="r.m.status === 'acked'" class="size-3.5 text-brand" aria-label="acknowledged" />
                      <span v-else-if="r.m.status === 'failed'" class="inline-flex items-center gap-1 text-bad"><CircleAlert class="size-3" />{{ sendError(r.m.error) }}</span>
                      <span class="max-sm:hidden">{{ r.m.status === 'acked' ? (current.kind === 'dm' ? 'delivered' : 'heard relayed') : r.m.status === 'failed' ? '' : r.m.status }}</span>
                    </template>
                  </div>
                </div>
              </div>
            </template>
          </div>

          <form class="border-t border-line-soft p-2.5 sm:p-3" @submit.prevent="send">
            <div v-if="identity && !identity.enabled" class="mb-2 rounded-lg bg-warn/10 px-3 py-1.5 text-xs text-warn">
              {{ identity.long_name }} is disabled. Enable it on the Identities page to send.
            </div>
            <div class="flex items-end gap-2">
              <div class="relative min-w-0 flex-1">
                <textarea
                  ref="composer"
                  v-model="text"
                  rows="1"
                  class="input max-h-32 min-h-9 resize-none pr-14 leading-snug"
                  :placeholder="current.kind === 'channel' ? `Message #${current.title}` : `Message ${current.title}`"
                  @keydown="onKey"
                />
                <span :class="['pointer-events-none absolute bottom-2.5 right-3 text-2xs tabular-nums', bytes > 200 ? 'text-bad' : 'text-ink-3']">{{ bytes }}/200</span>
              </div>
              <button class="btn btn-primary h-9 shrink-0 px-3" :disabled="!canSend" aria-label="Send">
                <Spinner v-if="sending" /><Send v-else class="size-4" />
              </button>
            </div>
          </form>
        </template>
        <div v-else class="empty flex-1">
          <Hash class="size-8 text-ink-3/60" />
          <p>Pick a conversation{{ identity ? ` for ${identity.long_name}` : '' }}, or start a direct message.</p>
        </div>
      </section>
    </div>

    <Modal :open="newDm" title="New direct message" :subtitle="identity ? `from ${identity.long_name}` : ''" @close="newDm = false">
      <div class="relative mb-3">
        <Search class="pointer-events-none absolute left-3 top-2.5 size-4 text-ink-3" />
        <input v-model="dmSearch" class="input pl-9" placeholder="Search nodes by name or id" autofocus />
      </div>
      <div class="-mx-2 max-h-80 overflow-y-auto">
        <button
          v-for="n in dmCandidates"
          :key="n.node_id"
          class="flex w-full items-center gap-3 rounded-xl px-2 py-1.5 text-left hover:bg-sunken"
          @click="startDm(n.node_id)"
        >
          <NodeAvatar :id="n.node_id" :short="n.short_name" size="sm" :local="n.local" />
          <span class="min-w-0 flex-1">
            <span class="block truncate text-[13px] font-medium">{{ n.long_name }}</span>
            <span class="mono block text-2xs text-ink-3">{{ n.node_id }} · {{ n.local ? 'local' : `heard ${relTime(n.last_heard, now)}` }}</span>
          </span>
          <Lock v-if="n.has_public_key" class="size-3.5 text-ink-3" aria-label="has public key" />
        </button>
      </div>
    </Modal>
  </div>
</template>
