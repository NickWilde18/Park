import { useState } from 'react'
import { api, problem, type Schemas } from './api/client'

// SyncButton asks a bridge Park runs for a round now. Once asked, it says the bridge is syncing until the bridge shows
// a round that ended after it was asked, as the view fetches it again on the bridge's notice; a refused ask is passed
// to failed, as what it says.
export function SyncButton({
  bridge,
  failed,
}: {
  bridge: { id: number; name: string; last_round?: Schemas['BridgeRound'] }
  failed: (said: string) => void
}) {
  // The end of the last round the bridge showed when it was asked, '' when it had none; null until asked.
  const [askedAfter, setAskedAfter] = useState<string | null>(null)
  const syncing = askedAfter !== null && askedAfter === (bridge.last_round?.ended_at ?? '')
  const ask = async () => {
    setAskedAfter(bridge.last_round?.ended_at ?? '')
    const { error } = await api.POST('/bridges/{id}/sync', { params: { path: { id: bridge.id } } })
    if (error) {
      setAskedAfter(null)
      failed(problem(error))
    }
  }
  return (
    <button disabled={syncing} onClick={() => void ask()}>
      {syncing ? `Syncing ${bridge.name}…` : `Sync ${bridge.name} now`}
    </button>
  )
}
