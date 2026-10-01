import { useEffect, useState } from 'react'

const listeners = new Set<() => void>()

// navigate moves to a path of the dashboard, with a fragment if it names one, without reloading the page.
export function navigate(to: string) {
  if (to !== window.location.pathname + window.location.hash) {
    window.history.pushState(null, '', to)
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
