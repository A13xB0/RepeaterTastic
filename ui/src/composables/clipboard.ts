import { toast } from './toast'

export async function copyText(text: string, label = 'Copied') {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // Fallback for http:// on a LAN, where the async clipboard API is unavailable.
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  }
  toast(label)
}
