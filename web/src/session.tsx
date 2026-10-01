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

// signInErrors say why a sign-in through a provider failed, by the code Park sends back in ?sign_in_error=.
const signInErrors: Record<string, string> = {
  denied: 'The forge did not let you sign in.',
  not_invited: 'This forge account is not a member of Park. Ask an admin for an invite.',
  invalid_invite: 'The invite is wrong, used, or expired. Ask an admin for a new one.',
  sign_in_expired: 'The sign-in took too long, or was started in another browser. Try again.',
  already_linked: 'That forge account belongs to another member. An admin can sort it out.',
  disabled: 'This member is disabled. Ask an admin.',
  not_found: 'There is no such way to sign in.',
}

// SignIn sets Park up with its setup code when it has no members yet; otherwise it signs a member in, with a local
// password or through a sign-in provider, or lets someone with an invite join, with a local account or through a
// provider. An invite's link, ?invite=, opens it ready to join; a failed sign-in through a provider, ?sign_in_error=,
// says why.
function SignIn({ onDone }: { onDone: () => void }) {
  const [query] = useState(() => new URLSearchParams(window.location.search))
  const [setup, setSetup] = useState(false)
  const [joining, setJoining] = useState(query.has('invite'))
  const [invite, setInvite] = useState(query.get('invite') ?? '')
  const [providers, setProviders] = useState<Schemas['AuthProvider'][]>([])
  const failed = query.get('sign_in_error')
  const [error, setError] = useState(failed ? (signInErrors[failed] ?? 'The sign-in did not work. Try again.') : '')
  useEffect(() => {
    void api.GET('/setup').then(({ data }) => setSetup(Boolean(data?.needed)))
    void api.GET('/auth/providers').then(({ data }) => setProviders(data?.providers ?? []))
  }, [])
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const form = new FormData(e.currentTarget)
    const field = (name: string) => String(form.get(name) ?? '')
    const person = { username: field('username'), password: field('password'), display_name: field('display_name') }
    const { error } = setup
      ? await api.POST('/setup', { body: { code: field('code'), ...person } })
      : joining
        ? await api.POST('/auth/register', { body: { invite: invite.trim(), ...person } })
        : await api.POST('/auth/login', { body: { username: person.username, password: person.password } })
    if (error) {
      setError(problem(error))
      return
    }
    window.history.replaceState(null, '', '/')
    onDone()
  }
  const creating = setup || joining
  const start = (p: Schemas['AuthProvider']) =>
    `/api/v1/auth/${encodeURIComponent(p.id)}/start` + (joining && invite.trim() ? `?invite=${encodeURIComponent(invite.trim())}` : '')
  return (
    <main className="sign-in">
      <h1>Park</h1>
      <form onSubmit={submit}>
        <h2>{setup ? 'Set Park up' : joining ? 'Join Park' : 'Sign in'}</h2>
        {setup && (
          <label>
            Setup code <input name="code" autoComplete="off" required />
          </label>
        )}
        {!setup && joining && (
          <label>
            Invite <input name="invite" autoComplete="off" value={invite} onChange={(e) => setInvite(e.target.value)} required />
          </label>
        )}
        <label>
          Username <input name="username" autoComplete="username" required />
        </label>
        <label>
          Password <input name="password" type="password" autoComplete={creating ? 'new-password' : 'current-password'} required />
        </label>
        {creating && (
          <label>
            Your name <input name="display_name" required />
          </label>
        )}
        <button type="submit">{setup ? 'Create the first admin' : joining ? 'Join' : 'Sign in'}</button>
        {error && <p className="error">{error}</p>}
      </form>
      {!setup && (
        <>
          {providers.map((p) => (
            <p key={p.id}>
              <a href={start(p)}>
                {joining ? 'Join' : 'Sign in'} with {p.name}
              </a>
            </p>
          ))}
          <p>
            <button type="button" className="link" onClick={() => setJoining(!joining)}>
              {joining ? 'Already a member? Sign in' : 'Have an invite? Join Park'}
            </button>
          </p>
        </>
      )}
    </main>
  )
}
