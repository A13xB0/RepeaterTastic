import { toast } from './toast'

export async function copyText(text: string, label = 'Copied') {
  try {
    await navigator.clipboard.writeText(text)
    toast(label)
  } catch {
    // The Clipboard API needs a secure context, which plain http:// on the LAN isn't: show the
    // text selected instead, ready for Ctrl+C.
    window.prompt('Press Ctrl+C (⌘C on a Mac) to copy, then Enter', text)
  }
}
