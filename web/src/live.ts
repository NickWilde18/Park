import { useEffect, useRef } from 'react'

type Listener = { kinds: string[]; call: () => void }

const listeners = new Set<Listener>()
let source: EventSource | null = null
const kindsHeard = new Set<string>()

// listen opens the one event stream the dashboard keeps, and hears each kind of notice once. Each time the stream
// opens, every view fetches again: what changed before the stream began is in what it fetches, and what changes
// after is sent. The browser resumes the stream by itself after a drop, from the last notice it saw; a stream that
// fell too far behind reloads the page.
function listen(kind: string) {
  if (!source) {
    source = new EventSource('/api/v1/events')
    source.addEventListener('open', () => listeners.forEach((l) => l.call()))
    source.addEventListener('reload', () => window.location.reload())
  }
  if (kindsHeard.has(kind)) return
  kindsHeard.add(kind)
  source.addEventListener(kind, () => {
    listeners.forEach((l) => {
      if (l.kinds.includes(kind)) l.call()
    })
  })
}

// useLive calls back whenever Park sends a notice of one of the kinds — verdict, task, group, or pr — so that a view
// fetches what it shows again.
export function useLive(kinds: string[], onNotice: () => void) {
  const call = useRef(onNotice)
  useEffect(() => {
    call.current = onNotice
  })
  const key = kinds.join(',')
  useEffect(() => {
    const listener: Listener = { kinds: key.split(','), call: () => call.current() }
    listener.kinds.forEach(listen)
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  }, [key])
}
