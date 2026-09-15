import { onBeforeUnmount, ref } from 'vue'

export function useMedia(query: string) {
  const mq = window.matchMedia(query)
  const matches = ref(mq.matches)
  const fn = (e: MediaQueryListEvent) => (matches.value = e.matches)
  mq.addEventListener('change', fn)
  onBeforeUnmount(() => mq.removeEventListener('change', fn))
  return matches
}
