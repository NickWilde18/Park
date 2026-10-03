import { useEffect, useRef, useState, type FormEvent } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { useFetch } from '../fetch'
import { roundText, since } from '../format'
import { useLive } from '../live'
import { useSession } from '../sessionContext'
import { SyncButton } from '../SyncButton'

type Answer = Promise<{ error?: unknown }>
type Bridge = Schemas['Bridge']
type Plugin = Schemas['BridgePlugin']
// Patch changes a bridge, saying said when it is changed, and answers whether it was.
type Patch = (b: Bridge, body: Schemas['BridgePatch'], said: string) => Promise<boolean>

// connectErrors say why connecting a bridge's account failed, by the code Park sends the browser back with.
const connectErrors: Record<string, string> = {
  denied: 'The platform did not let the account sign in.',
  connect_expired: 'Connecting the account took too long, or was started in another browser. Try again.',
  sign_in_refused: 'The platform did not confirm the sign-in. Try again.',
  credentials_missing: "Enter the bridge's credentials before connecting its account.",
  plugin_off: "The bridge's plugin is switched off.",
  plugin_missing: "Park no longer has the bridge's plugin.",
  plugin_failed: "The bridge's plugin failed; Park's log says why. Try again.",
  not_configured: 'Connecting an account needs http.public_url to be configured.',
}

// fields reads a submitted form's fields, trimmed, and keeps the page where it is.
function fields(e: FormEvent<HTMLFormElement>) {
  e.preventDefault()
  const form = new FormData(e.currentTarget)
  return (name: string) => String(form.get(name) ?? '').trim()
}

// Bridges is the bridges section of Connections, for an admin: the plugins Park has, each to switch on or off; every
// bridge, external or run by Park, each in a card, in the order they were registered; and a form to register one.
// Coming back from connecting a bridge's account, the page goes to this section, saying how it went.
export default function Bridges({ members }: { members: Schemas['Member'][] }) {
  const { me } = useSession()
  const [query] = useState(() => new URLSearchParams(window.location.search))
  const [notice, setNotice] = useState(() => {
    const failed = query.get('bridge_error')
    return failed === null ? '' : (connectErrors[failed] ?? 'Connecting the account did not work. Try again.')
  })
  const [connected, setConnected] = useState(() => Number(query.get('bridge_connected') ?? 0))
  const [token, setToken] = useState('')
  const heading = useRef<HTMLHeadingElement>(null)
  const { data, refresh } = useFetch(async () => {
    const [p, b] = await Promise.all([api.GET('/admin/bridge-plugins'), api.GET('/admin/bridges')])
    return { plugins: p.data?.plugins ?? [], bridges: b.data?.bridges ?? [] }
  })
  useLive(['bridge'], refresh)
  const arrived = data !== undefined && (query.has('bridge_connected') || query.has('bridge_error'))
  useEffect(() => {
    if (!arrived) return
    window.history.replaceState(null, '', window.location.pathname)
    heading.current?.scrollIntoView()
  }, [arrived])
  if (!data) return <p className="quiet">Loading…</p>
  const { plugins, bridges } = data
  const say = (said: string) => {
    setConnected(0)
    setNotice(said)
  }
  const done = async (answer: Answer, said: string) => {
    const { error } = await answer
    say(error ? problem(error) : said)
    refresh()
    return !error
  }
  const patch: Patch = (b, body, said) => done(api.PATCH('/admin/bridges/{id}', { params: { path: { id: b.id } }, body }), said)
  const memberName = (id: number) => members.find((m) => m.id === id)?.display_name ?? `member ${id}`
  const usable = plugins.filter((p) => p.installed && p.enabled)
  const shown = connected ? `Connected the account of ${bridges.find((b) => b.id === connected)?.name ?? 'the bridge'}.` : notice
  const bridgeToken = async (b: Bridge) => {
    const { data: made, error } = await api.POST('/me/tokens', { body: { name: `${b.name} bridge`, scopes: ['assertions:write'], bridge_id: b.id } })
    say(error ? problem(error) : '')
    setToken(made?.token ?? '')
  }
  return (
    <>
      <h2 ref={heading}>Bridges</h2>
      {shown && <p className="notice">{shown}</p>}
      {token && (
        <p className="notice">
          The bridge's token, shown this once: <code>{token}</code>
        </p>
      )}
      {plugins.length === 0 ? (
        <p className="quiet">Park has no bridge plugins: its plugin directory is not configured, or holds none.</p>
      ) : (
        <ul aria-label="Bridge plugins">
          {plugins.map((p) => (
            <li key={p.kind}>
              The {p.name} plugin is {p.enabled ? 'on' : 'off'}
              {!p.installed && ', and no longer in the plugin directory'}{' '}
              {(p.installed || p.enabled) && (
                <button
                  onClick={() =>
                    void done(
                      api.PUT('/admin/bridge-plugins/{kind}', { params: { path: { kind: p.kind } }, body: { enabled: !p.enabled } }),
                      `The ${p.name} plugin is ${p.enabled ? 'off' : 'on'}.`,
                    )
                  }
                >
                  Switch {p.name} {p.enabled ? 'off' : 'on'}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {bridges.map((b) =>
        b.plugin ? (
          <ParkRun key={b.id} bridge={b} plugin={plugins.find((p) => p.kind === b.plugin)} memberName={memberName} patch={patch} say={say} />
        ) : (
          <External
            key={b.id}
            bridge={b}
            owned={b.owner_id === me.member.id}
            usable={usable}
            memberName={memberName}
            patch={patch}
            createToken={() => void bridgeToken(b)}
          />
        ),
      )}
      <form
        className="inline"
        onSubmit={(e) => {
          const field = fields(e)
          void done(
            api.POST('/admin/bridges', {
              body: {
                name: field('name'),
                owner_id: Number(field('owner')),
                link_template: field('template') || undefined,
                plugin: field('plugin') || undefined,
              },
            }),
            `${field('name')} registered.`,
          )
        }}
      >
        <input name="name" placeholder="Name" aria-label="New bridge's name" required />
        <select name="owner" defaultValue={me.member.id} aria-label="New bridge's owner">
          {members.map((m) => (
            <option key={m.id} value={m.id}>
              {m.display_name}
            </option>
          ))}
        </select>
        <select name="plugin" defaultValue="" aria-label="New bridge's plugin">
          <option value="">External, with a token</option>
          {usable.map((p) => (
            <option key={p.kind} value={p.kind}>
              Run by Park, through {p.name}
            </option>
          ))}
        </select>
        <input name="template" placeholder="Link template, with {id}" aria-label="New bridge's link template" />
        <button type="submit">Register a bridge</button>
      </form>
    </>
  )
}

// External is a bridge that posts through Park's API with a token, in a card of its own: its owner and link template,
// a token for one the admin owns, its switch, and, while a plugin is usable, moving it into Park, which asks once
// more before it revokes its tokens.
function External({
  bridge: b,
  owned,
  usable,
  memberName,
  patch,
  createToken,
}: {
  bridge: Bridge
  owned: boolean
  usable: Plugin[]
  memberName: (id: number) => string
  patch: Patch
  createToken: () => void
}) {
  const [moving, setMoving] = useState(false)
  return (
    <article className="bridge" aria-label={b.name}>
      <h3>{b.name}</h3>
      <p>
        Owned by {memberName(b.owner_id)}, posting with a token
        {b.link_template && (
          <>
            , linking to <code>{b.link_template}</code>
          </>
        )}
        {!b.enabled && '; switched off, so its posts are refused'}.{' '}
        {owned && <button onClick={createToken}>Create a token for {b.name}</button>}{' '}
        <button onClick={() => void patch(b, { enabled: !b.enabled }, `${b.name} is ${b.enabled ? 'off' : 'on'}.`)}>
          Switch {b.name} {b.enabled ? 'off' : 'on'}
        </button>
      </p>
      {usable.length > 0 &&
        (moving ? (
          <form
            className="inline"
            onSubmit={(e) => {
              const field = fields(e)
              setMoving(false)
              void patch(b, { plugin: field('plugin') }, `Park runs ${b.name} now; its tokens are revoked.`)
            }}
          >
            <select name="plugin" aria-label={`Plugin to run ${b.name} through`}>
              {usable.map((p) => (
                <option key={p.kind} value={p.kind}>
                  {p.name}
                </option>
              ))}
            </select>
            <button type="submit">Move {b.name} into Park, and revoke its tokens</button>
            <button type="button" onClick={() => setMoving(false)}>
              Keep it outside
            </button>
          </form>
        ) : (
          <p>
            <button onClick={() => setMoving(true)}>Run {b.name} in Park</button>
          </p>
        ))}
    </article>
  )
}

// ParkRun is a bridge Park runs, in a card of its own: its owner and last round, to sync now while it is on; its
// switch; and its settings, one to a line — its plugin's credentials, typed in and never shown back, while Park has
// the plugin; the account it reads as, connected through the plugin's sign-in; the chat it reads, chosen from those
// the account can read; how often and how far back it reads; and its model. Secrets typed into a form are cleared once
// it is saved.
function ParkRun({
  bridge: b,
  plugin,
  memberName,
  patch,
  say,
}: {
  bridge: Bridge
  plugin?: Plugin
  memberName: (id: number) => string
  patch: Patch
  say: (said: string) => void
}) {
  const [chats, setChats] = useState<Schemas['BridgeChat'][]>()
  const listChats = async () => {
    const { data, error } = await api.GET('/admin/bridges/{id}/chats', { params: { path: { id: b.id } } })
    if (error) say(problem(error))
    else setChats(data.chats)
  }
  const resetOnSave = (form: HTMLFormElement) => (ok: boolean) => {
    if (ok) form.reset()
  }
  return (
    <article className="bridge" aria-label={b.name}>
      <h3>{b.name}</h3>
      <p>
        Owned by {memberName(b.owner_id)}, run by Park through {plugin?.name ?? b.plugin}
        {!b.enabled && ', switched off'}. {roundText(b)} {b.enabled && <SyncButton bridge={b} failed={say} />}{' '}
        <button onClick={() => void patch(b, { enabled: !b.enabled }, `${b.name} is ${b.enabled ? 'off' : 'on'}.`)}>
          Switch {b.name} {b.enabled ? 'off' : 'on'}
        </button>
      </p>
      {plugin && plugin.fields.length > 0 && (
        <div>
          <strong>Credentials</strong>{' '}
          <form
            className="inline"
            onSubmit={(e) => {
              const form = e.currentTarget
              const field = fields(e)
              const credentials: Record<string, string> = {}
              for (const f of plugin.fields) if (field(f.key)) credentials[f.key] = field(f.key)
              void patch(b, { credentials }, `${b.name}: credentials saved.`).then(resetOnSave(form))
            }}
          >
            {plugin.fields.map((f) => (
              <input
                key={f.key}
                name={f.key}
                type={f.secret ? 'password' : 'text'}
                autoComplete="off"
                placeholder={b.credentials.includes(f.key) ? `${f.label}: set` : f.label}
                aria-label={`${f.label} of ${b.name}`}
              />
            ))}
            <button type="submit">Save the credentials of {b.name}</button>
          </form>
        </div>
      )}
      <p>
        <strong>Account</strong>{' '}
        {b.account ? (
          <>
            Reads as {b.account.name}, connected by {memberName(b.account.connected_by)} {since(b.account.connected_at)} ago
            {b.account.lapsed_at && '; the platform no longer accepts it, so connect it again'}.{' '}
          </>
        ) : (
          'No account is connected. '
        )}
        <a href={`/api/v1/admin/bridges/${b.id}/connect`}>{b.account ? `Connect another account for ${b.name}` : `Connect an account for ${b.name}`}</a>
      </p>
      <div>
        <strong>Chat</strong> {b.chat ? `Reads the chat ${b.chat.name}. ` : 'No chat is chosen. '}
        <button onClick={() => void listChats()}>List the chats {b.name} can read</button>{' '}
        {chats &&
          (chats.length === 0 ? (
            <span className="quiet">The account can read no chats.</span>
          ) : (
            <form
              className="inline"
              onSubmit={(e) => {
                const id = fields(e)('chat')
                const chat = chats.find((c) => c.id === id)
                if (chat) void patch(b, { chat }, `${b.name} reads ${chat.name}.`)
              }}
            >
              <select name="chat" defaultValue={b.chat?.id} aria-label={`Chat ${b.name} reads`}>
                {chats.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
              <button type="submit">Save the chat of {b.name}</button>
            </form>
          ))}
      </div>
      <div>
        <strong>Schedule</strong>{' '}
        <form
          className="inline"
          onSubmit={(e) => {
            const field = fields(e)
            void patch(
              b,
              { every_minutes: Number(field('every')), lookback_days: Number(field('lookback')), propose_from: Number(field('propose')) },
              `${b.name}: schedule saved.`,
            )
          }}
        >
          Every
          <input name="every" type="number" min={1} max={1440} required defaultValue={b.every_minutes} aria-label={`Minutes between rounds of ${b.name}`} />
          minutes, reading threads from the last
          <input name="lookback" type="number" min={1} max={365} required defaultValue={b.lookback_days} aria-label={`Days back ${b.name} reads`} />
          days, and asking the model about threads that link
          <input
            name="propose"
            type="number"
            min={0}
            max={100}
            required
            defaultValue={b.propose_from}
            aria-label={`Pull requests a thread links before ${b.name} asks its model`}
          />
          or more pull requests (0 for never)
          <button type="submit">Save the schedule of {b.name}</button>
        </form>
      </div>
      <div>
        <strong>Model</strong>{' '}
        <form
          className="inline"
          onSubmit={(e) => {
            const form = e.currentTarget
            const field = fields(e)
            const model = { url: field('url'), name: field('name'), key: field('key') || undefined, timeout_seconds: Number(field('timeout')) }
            void patch(b, { model }, `${b.name}: model saved.`).then(resetOnSave(form))
          }}
        >
          <input name="url" defaultValue={b.model.url} placeholder="Model address, or none" aria-label={`Model address of ${b.name}`} />
          <input name="name" defaultValue={b.model.name} placeholder="Model name" aria-label={`Model name of ${b.name}`} />
          <input
            name="key"
            type="password"
            autoComplete="off"
            placeholder={b.model.key_set ? 'Model key: set' : 'Model key'}
            aria-label={`Model key of ${b.name}`}
          />
          <input
            name="timeout"
            type="number"
            min={1}
            max={600}
            required
            defaultValue={b.model.timeout_seconds}
            aria-label={`Seconds the model of ${b.name} has to answer`}
          />
          seconds to answer
          <button type="submit">Save the model of {b.name}</button>
        </form>
      </div>
    </article>
  )
}
