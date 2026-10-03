import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { useFetch } from '../fetch'
import { checkStates, pathText, prName, reasonsOf, since, words, type PRs } from '../format'
import { Link } from '../Link'
import { useLive } from '../live'
import { asked, refreshPRs } from '../refresh'
import { navigate } from '../router'
import { shownTheme, tellTheme, themedLink, useShownTheme } from '../theme'

type Props = { kind: 'group' | 'pull_request'; id: number }

// load fetches what a unit's page shows: its current verdict with its inputs, closed out or not; its pull requests'
// facts with their timelines; its tasks with their histories; and, for a group, the group. A group merged into another
// is answered as that other group: the page has moved there.
async function load(kind: Props['kind'], id: number) {
  let group: Schemas['Group'] | null = null
  let members = kind === 'pull_request' ? [id] : []
  if (kind === 'group') {
    const g = await api.GET('/groups/{id}', { params: { path: { id } } })
    if (g.data && g.data.id !== id) return { moved: g.data.id }
    group = g.data ?? null
    members = (g.data?.members ?? []).map((m) => m.pull_request)
  }
  const unit = `${kind === 'group' ? 'group' : 'pr'}:${id}`
  const [list, all, t] = await Promise.all([api.GET('/verdicts', { params: { query: { unit } } }), api.GET('/prs'), api.GET('/tasks')])
  const prs: PRs = new Map((all.data?.pull_requests ?? []).map((x) => [x.id, x]))
  const listed = (t.data?.tasks ?? []).filter((x) => x.unit.kind === kind && x.unit.id === id)
  const current = list.data?.verdicts[0]
  const verdict = current ? ((await api.GET('/verdicts/{id}', { params: { path: { id: current.id } } })).data ?? null) : null
  const got = await Promise.all(
    members.map(async (pr) => {
      const path = { params: { path: { id: pr } } }
      const [f, timeline] = await Promise.all([api.GET('/prs/{id}', path), api.GET('/prs/{id}/timeline', path)])
      return f.data ? [{ ...f.data, timeline: timeline.data?.events ?? [] }] : []
    }),
  )
  const facts = got.flat()
  // Merged and closed pull requests are not among the open ones listed; their own facts name them.
  for (const f of facts) prs.set(f.summary.id, f.summary)
  const tasks = await Promise.all(listed.map(async (x) => (await api.GET('/tasks/{id}', { params: { path: { id: x.id } } })).data ?? x))
  return { problem: list.error ? problem(list.error) : '', verdict, prs, tasks, group, facts }
}

// A unit: where its ball is and who holds it, or how it closed; every person's part with its path through the rules,
// the rule set version, each pull request's facts, when Park last checked them, and the timeline of its fact events,
// and its tasks with their histories. A group can be renamed, and its pull requests taken out. Its open pull requests
// can be refreshed from the forge. A group's description shows beside it, in a sidebar, and those who wrote one of
// its pull requests, and admins, may upload, replace, or remove it.
export default function Unit({ kind, id }: Props) {
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [rechecks, setRechecks] = useState(0)
  const { data, refresh } = useFetch(() => load(kind, id), `${kind}:${id}`)
  useLive(['verdict', 'task', 'pr', 'group'], refresh)
  const moved = data && 'moved' in data ? data.moved : 0
  useEffect(() => {
    if (moved) navigate(`/units/group/${moved}`, { replace: true })
  }, [moved])
  // A refresh that finds nothing new sends no notice, so the page looks again a little later, to show the check.
  useEffect(() => {
    if (!rechecks) return
    const t = window.setTimeout(refresh, 4000)
    return () => window.clearTimeout(t)
  }, [rechecks, refresh])

  const rename = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const name = String(new FormData(e.currentTarget).get('name') ?? '')
    const { error } = await api.PATCH('/groups/{id}', { params: { path: { id } }, body: { name } })
    if (error) setError(problem(error))
    else refresh()
  }
  const takeOut = async (pr: number) => {
    const { error } = await api.DELETE('/groups/{id}/members/{pr}', { params: { path: { id, pr } } })
    if (error) setError(problem(error))
    else navigate(`/units/pr/${pr}`)
  }
  const refreshOpen = async (open: number[]) => {
    const failed = await refreshPRs(open)
    if (failed) {
      setError(failed)
      return
    }
    setStatus(asked(open.length === 1 ? 'its pull request' : `its ${open.length} open pull requests`))
    setRechecks((n) => n + 1)
  }
  const described = (failed: string) => {
    setError(failed)
    if (!failed) refresh()
  }

  if (!data || 'moved' in data) return <p className="quiet">Loading…</p>
  const { verdict, prs, tasks, group, facts } = data
  const open = facts.filter((f) => f.summary.state === 'open' || f.summary.state === 'draft').map((f) => f.summary.id)
  const page = (
    <section>
      <h1>{group ? group.name : prName(prs, id)}</h1>
      {(error || data.problem) && <p className="error">{error || data.problem}</p>}
      {group && (
        <form onSubmit={rename} className="inline">
          <input name="name" defaultValue={group.name} aria-label="Group name" />
          <button type="submit">Rename</button>
        </form>
      )}
      {open.length > 0 && (
        <p>
          <button onClick={() => void refreshOpen(open)}>Refresh from the forge</button>{' '}
          <span role="status" className="quiet">
            {status}
          </span>
        </p>
      )}
      {group && !group.doc && group.doc_editable && (
        <section aria-label="Add a description">
          <h2>Description</h2>
          <p className="quiet">
            A static site, as a zip with its index.html at its top. Park serves it at a link of its own, and anyone who has the
            link can read it. Its pages follow the dashboard's theme through [data-theme="dark"] in their styles, and their
            default colors follow by themselves.
          </p>
          <DocForm id={group.id} label="Upload" done={described} />
        </section>
      )}
      {!verdict ? (
        <p className="quiet">No verdict yet.</p>
      ) : (
        <>
          <p className="ball">
            {verdict.closed ? `Closed out: ${verdict.closed}` : `The ball is on the ${verdict.side} side`}
            {verdict.fallback && <span className="badge stale"> the rules found nothing to do: fallback</span>}
            <span className="quiet"> · rule set <Link to="/rules">version {verdict.rule_version}</Link></span>
          </p>
          {verdict.holders.length > 0 && (
            <>
              <h2>Holding it</h2>
              <ul>
                {verdict.holders.map((h) => (
                  <li key={h.account.id}>
                    <strong>{h.account.login}</strong>, for {since(h.since)}:{' '}
                    {h.parts.map((p) => `${prName(prs, p.pull_request)} ${words(p.action ?? p.kind)} — ${p.reason}`).join('; ')}
                  </li>
                ))}
              </ul>
            </>
          )}
          <h2>Every part</h2>
          <table className="parts">
            <thead>
              <tr>
                <th>Pull request</th>
                <th>Person</th>
                <th>Part</th>
                <th>Path</th>
              </tr>
            </thead>
            <tbody>
              {verdict.parts.map((p) => (
                <tr key={`${p.pull_request}-${p.account.id}`}>
                  <td>{prName(prs, p.pull_request)}</td>
                  <td>{p.account.login}</td>
                  <td>
                    {words(p.kind)}
                    {p.action && ` · ${words(p.action)}`} — {p.reason}
                  </td>
                  <td className="path">{pathText(p)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
      <h2>Pull requests</h2>
      {facts.map((f) => (
        <article key={f.summary.id} className="pr">
          <h3>
            <a href={f.summary.url}>{prName(prs, f.summary.id)}</a> {f.summary.title}
            {f.summary.stale && <span className="badge stale">stale</span>}
            <span className="quiet"> · checked {since(f.summary.refreshed_at)} ago</span>
          </h3>
          <p>
            Checks:{' '}
            {f.facts.checks.map((c) => `${c.name} ${checkStates[c.state].text.toLowerCase()}`).join(', ') || 'none'}. Reviews:{' '}
            {f.facts.reviews.map((r) => `${login(f, r.author)} ${words(r.state)}`).join(', ') || 'none'}. Threads:{' '}
            {f.facts.threads.filter((t) => !t.resolved).length} open of {f.facts.threads.length}.
          </p>
          <details>
            <summary>Timeline ({f.timeline.length})</summary>
            <ol className="timeline" aria-label={`Timeline of ${prName(prs, f.summary.id)}`}>
              {f.timeline.map((e) => (
                <li key={e.seq}>
                  <span className="quiet">{since(e.observed_at)} ago</span> {e.seq === 1 ? 'first seen' : fieldsText(e.changes)}
                  <span className="quiet"> · {e.source}</span>
                </li>
              ))}
            </ol>
          </details>
          {group && (
            <button onClick={() => void takeOut(f.summary.id)}>Take {prName(prs, f.summary.id)} out of the group</button>
          )}
        </article>
      ))}
      <h2>Tasks</h2>
      {tasks.length === 0 && <p className="quiet">No tasks.</p>}
      {tasks.map((t) => (
        <article key={t.id} className="pr">
          <h3>
            <Link to={`/tasks#task-${t.id}`}>Task {t.id}</Link>: {words(t.state)}, attempt {t.attempt}
          </h3>
          <ul>
            {t.items.map((i) => (
              <li key={i.id}>
                {prName(prs, i.pull_request)} {words(i.action)} ({i.state}){i.failures.length > 0 && `: ${i.failures.join('; ')}`}
              </li>
            ))}
          </ul>
          <details>
            <summary>History ({t.events?.length ?? 0})</summary>
            <ol className="timeline" aria-label={`History of task ${t.id}`}>
              {(t.events ?? []).map((e) => (
                <li key={e.id}>
                  <span className="quiet">{since(e.at)} ago</span> {words(e.kind)}, {words(e.state)}
                  {reasonsOf(e).length > 0 && ` — ${reasonsOf(e).join('; ')}`}
                  <span className="quiet"> · by {e.actor}</span>
                </li>
              ))}
            </ol>
          </details>
        </article>
      ))}
    </section>
  )
  if (!group?.doc) return page
  return (
    <div className="with-doc">
      {page}
      <Description group={group} done={described} />
    </div>
  )
}

// sandbox is the sandbox a description's frame runs in, the same as Park's policy for /docs: scripts run, in an origin of
// their own, without the viewer's session.
const sandbox = 'allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads'

// uploadDoc sends a description's zip as it is, and answers Park's problem, or '' once it is stored.
async function uploadDoc(id: number, file: File): Promise<string> {
  const { error } = await api.PUT('/groups/{id}/doc', {
    params: { path: { id } },
    body: file as unknown as string,
    bodySerializer: (body: unknown) => body as BodyInit,
    headers: { 'Content-Type': 'application/zip' },
  })
  return error ? problem(error) : ''
}

// DocForm uploads a group's description from a zip the member chooses, replacing the one it has.
function DocForm({ id, label, done }: { id: number; label: string; done: (failed: string) => void }) {
  return (
    <form
      className="inline"
      onSubmit={(e) => {
        e.preventDefault()
        const file = new FormData(e.currentTarget).get('zip')
        if (!(file instanceof File) || file.size === 0) done('Choose a zip first.')
        else void uploadDoc(id, file).then(done)
      }}
    >
      <input type="file" name="zip" accept=".zip,application/zip" aria-label={`${label}: a zip of a static site`} />
      <button type="submit">{label}</button>
    </form>
  )
}

// Description is a group's description in a sidebar, framed in its sandbox, with a link to open it on its own and,
// for those who may, the controls to replace or remove it. It follows the dashboard's theme: the frame starts in the
// theme shown when the description loads, and is told the theme again whenever it changes or the frame loads a page,
// so that it does not reload, and a page reached inside it follows too.
function Description({ group, done }: { group: Schemas['Group']; done: (failed: string) => void }) {
  const doc = group.doc
  const theme = useShownTheme()
  const frame = useRef<HTMLIFrameElement>(null)
  const link = doc?.link ?? ''
  const src = useMemo(() => themedLink(link, shownTheme()), [link])
  useEffect(() => tellTheme(frame.current?.contentWindow, theme), [theme])
  if (!doc) return null
  const remove = async () => {
    const { error } = await api.DELETE('/groups/{id}/doc', { params: { path: { id: group.id } } })
    done(error ? problem(error) : '')
  }
  return (
    <aside className="doc" aria-label="Description">
      <header>
        <strong>Description</strong>
        <a href={themedLink(doc.link, theme)} target="_blank" rel="noopener noreferrer">
          Open in a new tab
        </a>
        <span className="quiet">
          by {doc.uploaded_by}, {since(doc.uploaded_at)} ago
        </span>
        {group.doc_editable && (
          <>
            <DocForm id={group.id} label="Replace" done={done} />
            <button onClick={() => void remove()}>Remove</button>
          </>
        )}
      </header>
      <iframe
        ref={frame}
        title={`Description of ${group.name}`}
        src={src}
        sandbox={sandbox}
        onLoad={(e) => tellTheme(e.currentTarget.contentWindow, theme)}
      />
    </aside>
  )
}

// fieldsText names the fields a fact event changed, for reading.
function fieldsText(changes: Record<string, unknown>): string {
  return Object.keys(changes).sort().map(words).join(', ')
}

function login(f: Schemas['PullRequest'], account: number): string {
  return f.accounts.find((a) => a.id === account)?.login ?? String(account)
}
