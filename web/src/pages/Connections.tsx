import { useState, type FormEvent, type ReactNode } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { useFetch } from '../fetch'
import { problemText, since } from '../format'
import { useLive } from '../live'
import Bridges from './Bridges'

type Answer = Promise<{ error?: unknown }>

// fields reads a submitted form's fields, trimmed, and keeps the page where it is.
function fields(e: FormEvent<HTMLFormElement>) {
  e.preventDefault()
  const form = new FormData(e.currentTarget)
  return (name: string) => String(form.get(name) ?? '').trim()
}

// Connections is what Park connects to, for an admin: the forges; the installations, with the address each forge
// delivers webhooks to and whether a secret is set, and whose credentials are typed in and never shown back; the
// repositories watched through them, each to refresh from its forge now; and the bridges (Bridges).
export default function Connections({ members }: { members: Schemas['Member'][] }) {
  const [notice, setNotice] = useState('')
  const { data, refresh } = useFetch(async () => {
    const [f, i, r] = await Promise.all([api.GET('/admin/forges'), api.GET('/admin/installations'), api.GET('/admin/repos')])
    return { forges: f.data?.forges ?? [], installations: i.data?.installations ?? [], repos: r.data?.repos ?? [] }
  })
  useLive(['installation'], refresh)
  const done = async (answer: Answer, said: string) => {
    const { error } = await answer
    setNotice(error ? problem(error) : said)
    refresh()
  }
  if (!data) return <p className="quiet">Loading…</p>
  const { forges, installations, repos } = data
  const forgeName = (id: number) => forges.find((f) => f.id === id)?.name ?? `forge ${id}`
  const installationName = (id: number) => installations.find((i) => i.id === id)?.name ?? `installation ${id}`
  const forgeOptions = forges.map((f) => (
    <option key={f.id} value={f.id}>
      {f.name}
    </option>
  ))
  return (
    <section aria-label="Connections">
      {notice && <p className="notice">{notice}</p>}
      <h2>Forges</h2>
      {forges.map((f) => (
        <form
          key={f.id}
          className="inline"
          onSubmit={(e) => {
            const field = fields(e)
            void done(api.PATCH('/admin/forges/{id}', { params: { path: { id: f.id } }, body: { name: field('name'), base_address: field('base') } }), `${field('name')}: saved.`)
          }}
        >
          <span className="quiet">{f.kind}</span>
          <input name="name" defaultValue={f.name} aria-label={`Name of forge ${f.id}`} required />
          <input name="base" defaultValue={f.base_address} aria-label={`Base address of forge ${f.id}`} required />
          <button type="submit" aria-label={`Save forge ${f.name}`}>
            Save
          </button>
        </form>
      ))}
      <form
        className="inline"
        onSubmit={(e) => {
          const field = fields(e)
          void done(api.POST('/admin/forges', { body: { kind: 'github', name: field('name'), base_address: field('base') } }), `${field('name')} added.`)
        }}
      >
        <input name="name" placeholder="Name" aria-label="New forge's name" required />
        <input name="base" placeholder="Base address" aria-label="New forge's base address" required />
        <button type="submit">Add a forge</button>
      </form>

      <h2>Installations</h2>
      <ul>
        {installations.map((i) => (
          <li key={i.id}>
            <strong>{i.name}</strong> on {forgeName(i.forge_id)}, with {i.kind === 'app' ? "an app's credential" : 'a token'}. Webhooks go to{' '}
            <code>{`${window.location.origin}${i.webhook_path}`}</code>
            {i.has_webhook_secret ? ', with a secret' : ', with no secret: Park takes no deliveries'}.
            {i.problem && i.problem_since && (
              <p className="notice problem" role="alert">
                {problemText({ problem: i.problem, since: i.problem_since, until: i.problem_until }, forgeName(i.forge_id))}
                {i.problem === 'refused' ? ': nothing is collected through it until its credential is replaced.' : '.'}
              </p>
            )}
            <Replace installation={i} done={done} />
          </li>
        ))}
      </ul>
      <NewInstallation forgeOptions={forgeOptions} done={done} />

      <h2>Repositories</h2>
      <ul>
        {repos.map((r) => (
          <li key={r.id}>
            <label>
              <input
                type="checkbox"
                checked={r.watched}
                aria-label={`Watch ${r.owner}/${r.name}`}
                onChange={(e) =>
                  void done(api.PATCH('/admin/repos/{id}', { params: { path: { id: r.id } }, body: { watched: e.target.checked } }), `${r.owner}/${r.name}: saved.`)
                }
              />{' '}
              {r.owner}/{r.name}
            </label>{' '}
            <span className="quiet">
              through {installationName(r.installation_id)}
              {r.refreshed_at ? `, its settings read ${since(r.refreshed_at)} ago` : ''}
            </span>{' '}
            {r.watched && (
              <button onClick={() => void done(api.POST('/prs/refresh', { params: { query: { repo: r.id } } }), `Asked the forge about ${r.owner}/${r.name}.`)}>
                Refresh {r.owner}/{r.name}
              </button>
            )}
          </li>
        ))}
      </ul>
      <form
        className="inline"
        onSubmit={(e) => {
          const field = fields(e)
          void done(
            api.POST('/admin/repos', { body: { installation_id: Number(field('installation')), owner: field('owner'), name: field('name') } }),
            `${field('owner')}/${field('name')} is watched.`,
          )
        }}
      >
        <select name="installation" aria-label="Installation to watch through">
          {installations.map((i) => (
            <option key={i.id} value={i.id}>
              {i.name}
            </option>
          ))}
        </select>
        <input name="owner" placeholder="Owner" aria-label="Repository owner" required />
        <input name="name" placeholder="Name" aria-label="Repository name" required />
        <button type="submit">Watch</button>
      </form>

      <Bridges members={members} />
    </section>
  )
}

// NewInstallation adds an installation: a forge, a name, and a token or an app's credential, with an optional webhook
// secret.
function NewInstallation({ forgeOptions, done }: { forgeOptions: ReactNode; done: (answer: Answer, said: string) => Promise<void> }) {
  const [kind, setKind] = useState<'token' | 'app'>('token')
  return (
    <form
      className="installation"
      onSubmit={(e) => {
        const field = fields(e)
        const body: Schemas['InstallationRequest'] = { forge_id: Number(field('forge')), name: field('name'), kind }
        if (kind === 'token') body.token = field('token')
        else body.app = { app_id: Number(field('app_id')), installation_id: Number(field('app_installation')), private_key: field('key') }
        if (field('secret')) body.webhook_secret = field('secret')
        void done(api.POST('/admin/installations', { body }), `${field('name')} added.`)
      }}
    >
      <select name="forge" aria-label="New installation's forge">
        {forgeOptions}
      </select>
      <input name="name" placeholder="Name, as the organization" aria-label="New installation's name" required />
      <select value={kind} onChange={(e) => setKind(e.target.value === 'app' ? 'app' : 'token')} aria-label="New installation's credential">
        <option value="token">A token</option>
        <option value="app">An app</option>
      </select>
      {kind === 'token' ? (
        <input name="token" type="password" autoComplete="off" placeholder="Token" aria-label="New installation's token" required />
      ) : (
        <>
          <input name="app_id" inputMode="numeric" placeholder="App ID" aria-label="New installation's app ID" required />
          <input name="app_installation" inputMode="numeric" placeholder="Installation ID" aria-label="New installation's app installation ID" required />
          <textarea name="key" placeholder="Private key, as PEM" aria-label="New installation's private key" required />
        </>
      )}
      <input name="secret" type="password" autoComplete="off" placeholder="Webhook secret, if any" aria-label="New installation's webhook secret" />
      <button type="submit">Add an installation</button>
    </form>
  )
}

// Replace replaces an installation's credential or webhook secret; a field left empty keeps what the installation
// has, and clearing the secret stops Park taking deliveries.
function Replace({ installation, done }: { installation: Schemas['Installation']; done: (answer: Answer, said: string) => Promise<void> }) {
  const i = installation
  return (
    <form
      className="inline"
      onSubmit={(e) => {
        const field = fields(e)
        const body: Schemas['InstallationPatch'] = {}
        if (i.kind === 'token' && field('token')) body.token = field('token')
        if (i.kind === 'app' && field('key')) body.app = { app_id: Number(field('app_id')), installation_id: Number(field('app_installation')), private_key: field('key') }
        if (field('secret')) body.webhook_secret = field('secret')
        if (field('clear') === 'yes') body.webhook_secret = ''
        void done(api.PATCH('/admin/installations/{id}', { params: { path: { id: i.id } }, body }), `${i.name}: saved.`)
      }}
    >
      {i.kind === 'token' ? (
        <input name="token" type="password" autoComplete="off" placeholder="New token" aria-label={`New token of ${i.name}`} />
      ) : (
        <>
          <input name="app_id" inputMode="numeric" placeholder="App ID" aria-label={`App ID of ${i.name}`} />
          <input name="app_installation" inputMode="numeric" placeholder="Installation ID" aria-label={`App installation ID of ${i.name}`} />
          <textarea name="key" placeholder="New private key, as PEM" aria-label={`New private key of ${i.name}`} />
        </>
      )}
      <input name="secret" type="password" autoComplete="off" placeholder="New webhook secret" aria-label={`New webhook secret of ${i.name}`} />
      {i.has_webhook_secret && (
        <label>
          <input type="checkbox" name="clear" value="yes" /> stop taking deliveries
        </label>
      )}
      <button type="submit" aria-label={`Save installation ${i.name}`}>
        Save
      </button>
    </form>
  )
}
