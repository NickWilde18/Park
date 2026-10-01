import { useState, type FormEvent } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { useFetch } from '../fetch'
import { words } from '../format'
import { useSession } from '../sessionContext'
import Administration from './Admin'

const taskActions = ['resolve_conflicts', 'fix_checks', 'address_comments', 'request_review', 'review']

// My settings: which of my actions become tasks, and in which repositories; my API tokens; and my linked accounts,
// which I can unlink, unless one is my last way in, or add through a sign-in provider. An admin also keeps Park's
// members, invites, and sign-in providers here.
export default function Settings() {
  const { me } = useSession()
  const [created, setCreated] = useState('')
  const [notice, setNotice] = useState('')
  const { data, refresh } = useFetch(async () => {
    const [a, t, i, p, v] = await Promise.all([
      api.GET('/me/automation'),
      api.GET('/me/tokens'),
      api.GET('/me/identities'),
      api.GET('/prs'),
      api.GET('/auth/providers'),
    ])
    const automation: Schemas['Automation'] = a.data ?? { actions: [] }
    // The repositories to choose from: those of the pull requests Park collects, and any the automation names already.
    const repos = new Map((p.data?.pull_requests ?? []).map((x) => [x.repo.id, `${x.repo.owner}/${x.repo.name}`]))
    for (const id of automation.repos ?? []) if (!repos.has(id)) repos.set(id, `repository ${id}`)
    return {
      automation,
      repos: [...repos].sort((x, y) => x[1].localeCompare(y[1])),
      tokens: t.data?.tokens ?? [],
      identities: i.data?.identities ?? [],
      providers: v.data?.providers ?? [],
    }
  })
  const saveAutomation = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const form = new FormData(e.currentTarget)
    const actions = form.getAll('action').map(String)
    const repos = form.get('where') === 'these' ? form.getAll('repo').map(Number) : undefined
    const { error } = await api.PUT('/me/automation', { body: { actions, repos } })
    setNotice(error ? problem(error) : 'Automation saved.')
    refresh()
  }
  const createToken = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const form = new FormData(e.currentTarget)
    const scopes = form.getAll('scope').map(String) as Schemas['TokenRequest']['scopes']
    const { data, error } = await api.POST('/me/tokens', { body: { name: String(form.get('name')), scopes } })
    if (error) setNotice(problem(error))
    else setCreated(data.token)
    refresh()
  }
  const revoke = async (id: string) => {
    const { error } = await api.DELETE('/me/tokens/{token}', { params: { path: { token: id } } })
    setNotice(error ? problem(error) : 'Token revoked.')
    refresh()
  }
  const unlink = async (id: number) => {
    const { error } = await api.DELETE('/me/identities/{id}', { params: { path: { id } } })
    setNotice(error ? problem(error) : 'Account unlinked.')
    refresh()
  }
  if (!data) return <p className="quiet">Loading…</p>
  const { automation, repos, tokens, identities, providers } = data
  return (
    <section>
      <h1>Settings</h1>
      {notice && <p className="notice">{notice}</p>}
      <h2>Automation</h2>
      <form onSubmit={saveAutomation}>
        <fieldset>
          <legend>These actions of mine become tasks</legend>
          {taskActions.map((a) => (
            <label key={a}>
              <input type="checkbox" name="action" value={a} defaultChecked={automation.actions.includes(a)} /> {words(a)}
            </label>
          ))}
        </fieldset>
        <fieldset>
          <legend>In</legend>
          <label>
            <input type="radio" name="where" value="every" defaultChecked={!automation.repos} /> every repository
          </label>
          <label>
            <input type="radio" name="where" value="these" defaultChecked={!!automation.repos} /> only these repositories:
          </label>
          {repos.map(([id, name]) => (
            <label key={id} className="nested">
              <input type="checkbox" name="repo" value={id} defaultChecked={automation.repos?.includes(id)} /> {name}
            </label>
          ))}
        </fieldset>
        <button type="submit">Save</button>
      </form>
      <h2>API tokens</h2>
      {created && (
        <p className="notice">
          Your new token, shown this once: <code>{created}</code>
        </p>
      )}
      <ul>
        {tokens.map((t) => (
          <li key={t.id}>
            {t.name} ({t.scopes.join(', ')}){t.revoked_at ? ' — revoked' : <button onClick={() => void revoke(t.id)}>Revoke</button>}
          </li>
        ))}
      </ul>
      <form onSubmit={createToken} className="inline">
        <input name="name" placeholder="Name" aria-label="Token name" required />
        <label>
          <input type="checkbox" name="scope" value="read" defaultChecked /> read
        </label>
        <label>
          <input type="checkbox" name="scope" value="tasks:work" /> tasks:work
        </label>
        <button type="submit">Create token</button>
      </form>
      <h2>Linked accounts</h2>
      <ul>
        {identities.map((i) => (
          <li key={i.id}>
            {i.login} on {i.forge_name}{' '}
            <button aria-label={`Unlink ${i.login} on ${i.forge_name}`} onClick={() => void unlink(i.id)}>
              Unlink
            </button>
          </li>
        ))}
      </ul>
      {providers.map((p) => (
        <p key={p.id}>
          <a href={`/api/v1/auth/${encodeURIComponent(p.id)}/start?mode=link`}>Connect an account on {p.name}</a>
        </p>
      ))}
      {me.member.admin && <Administration changed={refresh} />}
    </section>
  )
}
