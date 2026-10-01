import { useEffect, useState } from 'react'

const listeners = new Set<() => void>()

// navigate moves to a path of the dashboard, with a fragment if it names one, without reloading the page. With
// replace, the path takes the place of the current one in the browser's history, as when a page has moved.
export function navigate(to: string, { replace = false } = {}) {
  if (to !== window.location.pathname + window.location.hash) {
    if (replace) window.history.replaceState(null, '', to)
    else window.history.pushState(null, '', to)
    listeners.forEach((listen) => listen())
  }
}

// usePath is the path the dashboard shows now.
export function usePath(): string {
  const [path, setPath] = useState(window.location.pathname)
  useEffect(() => {
    const update = () => setPath(window.location.pathname)
    listeners.add(update)
    window.addEventListener('popstate', update)
    return () => {
      listeners.delete(update)
      window.removeEventListener('popstate', update)
    }
  }, [])
  return path
}
