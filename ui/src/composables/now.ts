import { ref } from 'vue'

/** A shared clock for relative times; ticks every second but only one timer exists. */
export const now = ref(Date.now())
setInterval(() => (now.value = Date.now()), 1000)
