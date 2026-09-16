<script setup lang="ts">
// Identities behind a board on Meshtastic firmware reach the air over its MQTT client proxy. The
// firmware marks what it repeats from there as "via MQTT", so the mesh sees them that way.
withDefaults(defineProps<{ compact?: boolean }>(), { compact: false })
</script>

<template>
  <div role="note" :class="['flex items-start gap-2 rounded-lg border border-warn/35 bg-warn/8 text-ink-2', compact ? 'px-2.5 py-1.5 text-2xs' : 'px-3 py-2 text-xs']">
    <span class="mt-px shrink-0 whitespace-nowrap font-semibold text-warn">Heads up</span>
    <span>
      Your identities reach the air through the board's MQTT client proxy, so the board sends their packets marked <b class="font-semibold text-ink">via MQTT</b>.
      Apps and other nodes show them as MQTT traffic, and nodes set to ignore MQTT drop them. The board's own packets aren't affected.
      Direct messages <b class="font-semibold text-ink">to</b> your identities don't arrive: the board only passes on what it can read. Channel messages and their direct messages out work.
    </span>
  </div>
</template>
