import { useSyncExternalStore } from 'react'

// Theme is the dashboard's look: system, which follows the operating system, light, or dark.
export type Theme = 'system' | 'light' | 'dark'

// Shown is the theme a page shows: light or dark.
export type Shown = 'light' | 'dark'

const key = 'park.theme'
const changed = 'park-theme'
const dark = () => window.matchMedia('(prefers-color-scheme: dark)')

// storedTheme is the theme this browser chose, system unless it chose another.
export function storedTheme(): Theme {
  const t = localStorage.getItem(key)
  return t === 'light' || t === 'dark' ? t : 'system'
}

// applyTheme shows a theme. The stylesheet picks its colors from the root's color-scheme, which data-theme sets.
export function applyTheme(t: Theme) {
  if (t === 'system') delete document.documentElement.dataset.theme
  else document.documentElement.dataset.theme = t
}

// chooseTheme shows a theme, keeps the choice in this browser, and tells whoever watches the shown theme.
export function chooseTheme(t: Theme) {
  if (t === 'system') localStorage.removeItem(key)
  else localStorage.setItem(key, t)
  applyTheme(t)
  window.dispatchEvent(new Event(changed))
}

// shownTheme is the theme shown now: the one chosen, or for system, the operating system's.
export function shownTheme(): Shown {
  const t = storedTheme()
  if (t !== 'system') return t
  return dark().matches ? 'dark' : 'light'
}

// watchTheme calls back whenever the shown theme may have changed: the choice, or the operating system's while the
// choice is system. It returns the function that stops watching.
export function watchTheme(callback: () => void): () => void {
  const media = dark()
  window.addEventListener(changed, callback)
  media.addEventListener('change', callback)
  return () => {
    window.removeEventListener(changed, callback)
    media.removeEventListener('change', callback)
  }
}

// useShownTheme is the theme shown now, kept current.
export function useShownTheme(): Shown {
  return useSyncExternalStore(watchTheme, shownTheme)
}

// themedLink is a description's link that tells its pages the theme to start in: they follow it through the script
// Park serves them with (internal/docs/theme.go).
export function themedLink(link: string, theme: Shown): string {
  return `${link}?theme=${theme}`
}

// tellTheme tells a description in a frame the theme shown now, as its pages listen for it. The frame's origin is
// opaque, so the message is addressed to any origin; it holds nothing but the theme.
export function tellTheme(frame: Window | null | undefined, theme: Shown) {
  frame?.postMessage({ type: 'park-theme', theme }, '*')
}
