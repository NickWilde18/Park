import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { api, problem, setCSRF, type Schemas } from './api/client'
import { useFetch } from './fetch'
import { SessionContext, type Session } from './sessionContext'

async function signedIn(): Promise<Session | null> {
  const { data } = await api.GET('/me')
  if (!data) return null
  setCSRF(data.csrf_token ?? '')
  const ids = await api.GET('/me/identities')
  return { me: data, accounts: new Set((ids.data?.identities ?? []).map((i) => i.account_id)) }
}

// SessionGate shows its children to a signed-in member, and the sign-in page to anyone else.
export function SessionGate({ children }: { children: ReactNode }) {
  const { data: session, refresh } = useFetch(signedIn)
  if (session === undefined) return <p className="quiet">Loading…</p>
  if (!session) return <SignIn onDone={refresh} />
  return <SessionContext.Provider value={session}>{children}</SessionContext.Provider>
}

// SignIn sets Park up with its setup code when it has no members yet, and otherwise signs a member in, with a local
// password or through a sign-in provider.
function SignIn({ onDone }: { onDone: () => void }) {
  const [setup, setSetup] = useState(false)
  const [providers, setProviders] = useState<Schemas['AuthProvider'][]>([])
  const [error, setError] = useState('')
  useEffect(() => {
    void api.GET('/setup').then(({ data }) => setSetup(Boolean(data?.needed)))
    void api.GET('/auth/providers').then(({ data }) => setProviders(data?.providers ?? []))
  }, [])
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const form = new FormData(e.currentTarget)
    const field = (name: string) => String(form.get(name) ?? '')
    const { error } = setup
      ? await api.POST('/setup', {
          body: { code: field('code'), username: field('username'), password: field('password'), display_name: field('display_name') },
        })
      : await api.POST('/auth/login', { body: { username: field('username'), password: field('password') } })
    if (error) {
      setError(problem(error))
      return
    }
    onDone()
  }
  return (
    <main className="sign-in">
      <h1>Park</h1>
      <form onSubmit={submit}>
        <h2>{setup ? 'Set Park up' : 'Sign in'}</h2>
        {setup && (
          <label>
            Setup code <input name="code" autoComplete="off" required />
          </label>
        )}
        <label>
          Username <input name="username" autoComplete="username" required />
        </label>
        <label>
          Password <input name="password" type="password" autoComplete={setup ? 'new-password' : 'current-password'} required />
        </label>
        {setup && (
          <label>
            Your name <input name="display_name" required />
          </label>
        )}
        <button type="submit">{setup ? 'Create the first admin' : 'Sign in'}</button>
        {error && <p className="error">{error}</p>}
      </form>
      {!setup &&
        providers.map((p) => (
          <p key={p.id}>
            <a href={`/api/v1/auth/${encodeURIComponent(p.id)}/start`}>Sign in with {p.name}</a>
          </p>
        ))}
    </main>
  )
}
