import type { MouseEvent, ReactNode } from 'react'
import { navigate } from './router'

// Link is a link within the dashboard; opened in a new tab, it is an ordinary link.
export function Link({ to, children }: { to: string; children: ReactNode }) {
  const onClick = (e: MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return
    e.preventDefault()
    navigate(to)
  }
  return (
    <a href={to} onClick={onClick}>
      {children}
    </a>
  )
}
