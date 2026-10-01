import { createContext, useContext } from 'react'
import type { Schemas } from './api/client'

// Session is the signed-in member, with the forge accounts linked to them: the accounts whose parts are theirs.
export type Session = { me: Schemas['Me']; accounts: Set<number> }

export const SessionContext = createContext<Session | null>(null)

// useSession is the signed-in member's session.
export function useSession(): Session {
  const s = useContext(SessionContext)
  if (!s) throw new Error('useSession outside a session')
  return s
}
