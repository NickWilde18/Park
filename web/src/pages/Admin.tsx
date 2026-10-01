import { useState, type FormEvent } from 'react'
import { api, problem } from '../api/client'
import { useFetch } from '../fetch'
import { since } from '../format'
import Connections from './Connections'

type Answer = Promise<{ error?: unknown }>

// Administration is what an admin keeps in Settings: the members, with their permissions and the accounts linked to
// them by hand; the invites, whose code is shown once; the sign-in providers, with the callback address each forge
// needs; and what Park connects to (Connections). Park keeps at least one active admin, and says so when a change would leave none. After each change it
// tells Settings, whose own parts, such as the providers to connect an account through, may have changed with it.
export default function Administration({ changed }: { changed: () => void }) {
  const [notice, setNotice] = useState('')
  const [invite, setInvite] = useState('')
  const { data, refresh } = useFetch(async () => {
    const [m, i, p, f] = await Promise.all([api.GET('/admin/members'), api.GET('/admin/invites'), api.GET('/admin/providers'), api.GET('/admin/forges')])
    return { members: m.data?.members ?? [], invites: i.data?.invites ?? [], providers: p.data?.providers ?? [], forges: f.data?.forges ?? [] }
  })
  const done = async (answer: Answer, said: string) => {
    const { error } = await answer
    setNotice(error ? problem(error) : said)
    refresh()
    changed()
  }
  if (!data) return <p className="quiet">Loading…</p>
  const { members, invites, providers, forges } = data
  const memberName = (id?: number | null) => members.find((m) => m.id === id)?.display_name ?? `member ${id}`
  const forgeName = (id: number) => forges.find((f) => f.id === id)?.name ?? `forge ${id}`
  const form = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const fields = new FormData(e.currentTarget)
    return (name: string) => String(fields.get(name) ?? '').trim()
  }
  const createInvite = async (e: FormEvent<HTMLFormElement>) => {
    const field = form(e)
    const { data: made, error } = await api.POST('/admin/invites', { body: { expires_in_hours: Number(field('days')) * 24 } })
    setNotice(error ? problem(error) : '')
    setInvite(made?.code ?? '')
    refresh()
  }
  const link = `${window.location.origin}/?invite=${encodeURIComponent(invite)}`
  return (
    <section aria-label="Administration">
      <h2>Members</h2>
      {notice && <p className="notice">{notice}</p>}
      <table className="members">
        <thead>
          <tr>
            <th>Member</th>
            <th>Admin</th>
            <th>Publishes rules</th>
            <th>Disabled</th>
            <th>Link an account by hand</th>
          </tr>
        </thead>
        <tbody>
          {members.map((m) => {
            const path = { params: { path: { id: m.id } } }
            const flag = (key: 'admin' | 'rules' | 'disabled', label: string) => (
              <input
                type="checkbox"
                aria-label={`${m.display_name} ${label}`}
                checked={m[key]}
                onChange={(e) => void done(api.PATCH('/admin/members/{id}', { ...path, body: { [key]: e.target.checked } }), `${m.display_name}: saved.`)}
              />
            )
            return (
              <tr key={m.id}>
                <td>{m.display_name}</td>
                <td>{flag('admin', 'is an admin')}</td>
                <td>{flag('rules', 'publishes rules')}</td>
                <td>{flag('disabled', 'is disabled')}</td>
                <td>
                  <form
                    className="inline"
                    onSubmit={(e) => {
                      const field = form(e)
                      void done(
                        api.POST('/admin/members/{id}/identities', { ...path, body: { forge_id: Number(field('forge')), account_id: Number(field('account')) } }),
                        `${m.display_name}: account linked.`,
                      )
                    }}
                  >
                    <select name="forge" aria-label={`Forge of the account to link to ${m.display_name}`}>
                      {forges.map((f) => (
                        <option key={f.id} value={f.id}>
                          {f.name}
                        </option>
                      ))}
                    </select>
                    <input name="account" inputMode="numeric" pattern="[0-9]+" placeholder="Account ID" aria-label={`Account ID to link to ${m.display_name}`} required />
                    <button type="submit">Link</button>
                  </form>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      <p className="quiet">An account is linked by its numeric ID on the forge, never by its login: a login can change hands.</p>

      <h2>Invites</h2>
      {invite && (
        <p className="notice">
          The invite, shown this once: <code>{invite}</code>. Its link opens the sign-in page ready to join: <code>{link}</code>
        </p>
      )}
      <form className="inline" onSubmit={(e) => void createInvite(e)}>
        <label>
          Valid for <input name="days" type="number" min={1} max={30} defaultValue={7} aria-label="Days the invite is valid" /> days
        </label>
        <button type="submit">Create an invite</button>
      </form>
      <ul>
        {invites.map((i) => (
          <li key={i.id}>
            Invite {i.id}, made {since(i.created_at)} ago by {memberName(i.created_by)}
            {i.used_at ? (
              `: used by ${memberName(i.used_by)} ${since(i.used_at)} ago`
            ) : (
              <>
                , good until {new Date(i.expires_at).toLocaleString()}{' '}
                <button onClick={() => void done(api.DELETE('/admin/invites/{id}', { params: { path: { id: i.id } } }), `Invite ${i.id} revoked.`)}>
                  Revoke
                </button>
              </>
            )}
          </li>
        ))}
      </ul>

      <h2>Sign-in providers</h2>
      {providers.length === 0 && <p className="quiet">None: members sign in with a local password.</p>}
      {providers.map((p) => (
        <article key={p.id} className="provider">
          <h3>
            {p.name} <span className="quiet">({p.id}, on {forgeName(p.forge_id)})</span>
          </h3>
          <p>
            The forge's application calls back to <code>{`${window.location.origin}/api/v1/auth/${encodeURIComponent(p.id)}/callback`}</code>
          </p>
          <form
            className="inline"
            onSubmit={(e) => {
              const field = form(e)
              const body: { name?: string; client_id?: string; client_secret?: string } = {}
              if (field('name')) body.name = field('name')
              if (field('client_id')) body.client_id = field('client_id')
              if (field('client_secret')) body.client_secret = field('client_secret')
              void done(api.PATCH('/admin/providers/{provider}', { params: { path: { provider: p.id } }, body }), `${p.name}: saved.`)
            }}
          >
            <input name="name" defaultValue={p.name} aria-label={`Name of ${p.id}`} />
            <input name="client_id" defaultValue={p.client_id} aria-label={`Client ID of ${p.id}`} />
            <input name="client_secret" type="password" autoComplete="off" placeholder="New client secret" aria-label={`New client secret of ${p.id}`} />
            <button type="submit" aria-label={`Save provider ${p.name}`}>
              Save
            </button>
          </form>{' '}
          <button onClick={() => void done(api.DELETE('/admin/providers/{provider}', { params: { path: { provider: p.id } } }), `${p.name} removed.`)}>
            Remove {p.name}
          </button>
        </article>
      ))}
      <form
        className="inline"
        onSubmit={(e) => {
          const field = form(e)
          void done(
            api.POST('/admin/providers', {
              body: { id: field('id'), kind: 'forge', name: field('name'), forge_id: Number(field('forge')), client_id: field('client_id'), client_secret: field('client_secret') },
            }),
            `${field('name')} added.`,
          )
        }}
      >
        <input name="id" placeholder="ID, as in its callback address" aria-label="Provider ID" required />
        <input name="name" placeholder="Name on the sign-in page" aria-label="Provider name" required />
        <select name="forge" aria-label="Provider's forge">
          {forges.map((f) => (
            <option key={f.id} value={f.id}>
              {f.name}
            </option>
          ))}
        </select>
        <input name="client_id" placeholder="Client ID" aria-label="Provider client ID" required />
        <input name="client_secret" type="password" autoComplete="off" placeholder="Client secret" aria-label="Provider client secret" required />
        <button type="submit">Add a provider</button>
      </form>
      <Connections members={members} />
    </section>
  )
}
