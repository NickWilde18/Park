import { useEffect, useRef } from 'react'
import { api } from './api/client'

type Listener = { kinds: string[]; call: () => void }

const listeners = new Set<Listener>()
const kindsHeard = new Set<string>()
let source: EventSource | null = null
let reopenIn = 1000

// hear passes one kind of notice from the stream to the views that listen for it.
function hear(kind: string) {
  source?.addEventListener(kind, () => {
    listeners.forEach((l) => {
      if (l.kinds.includes(kind)) l.call()
    })
  })
}

// open opens the one event stream the dashboard keeps. Each time it opens, every view fetches again: what changed
// before the stream began is in what it fetches, and what changes after is sent. The browser resumes a stream by
// itself after a drop, from the last notice it saw; a stream that fell too far behind reloads the page. A stream the
// browser gives up on — its resumption was answered with an error, as while Park restarts — is opened again after a
// second, then after twice as long each time, up to thirty; unless the session has ended, when the page reloads, to
// sign in.
function open() {
  const opened = new EventSource('/api/v1/events')
  source = opened
  opened.addEventListener('open', () => {
    reopenIn = 1000
    listeners.forEach((l) => l.call())
  })
  opened.addEventListener('reload', () => window.location.reload())
  opened.addEventListener('error', () => {
    if (opened.readyState !== EventSource.CLOSED || source !== opened) return
    source = null
    setTimeout(() => void reopen(), reopenIn)
    reopenIn = Math.min(reopenIn * 2, 30000)
  })
  kindsHeard.forEach(hear)
}

async function reopen() {
  try {
    const { response } = await api.GET('/me')
    if (response.status === 401) {
      window.location.reload()
      return
    }
  } catch {
    // Park cannot be reached yet: the stream's own failure will try again.
  }
  open()
}

function listen(kind: string) {
  if (!source) open()
  if (kindsHeard.has(kind)) return
  kindsHeard.add(kind)
  hear(kind)
}

// useLive calls back whenever Park sends a notice of one of the kinds — verdict, task, group, or pr — so that a view
// fetches what it shows again, and whenever the stream opens.
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
