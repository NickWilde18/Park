import { useState, type FormEvent } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { useFetch } from '../fetch'
import { pathText, prName, since, words, type PRs } from '../format'
import { Link } from '../Link'
import { useLive } from '../live'
import { navigate } from '../router'

type Props = { kind: 'group' | 'pull_request'; id: number }

// load fetches what a unit's page shows: its current verdict with its inputs, its pull requests' facts with their
// timelines, its tasks with their histories, and, for a group, the group.
async function load(kind: Props['kind'], id: number) {
  const [list, all, t] = await Promise.all([api.GET('/verdicts'), api.GET('/prs'), api.GET('/tasks')])
  const prs: PRs = new Map((all.data?.pull_requests ?? []).map((x) => [x.id, x]))
  const listed = (t.data?.tasks ?? []).filter((x) => x.unit.kind === kind && x.unit.id === id)
  let group: Schemas['Group'] | null = null
  let members = kind === 'pull_request' ? [id] : []
  if (kind === 'group') {
    const g = await api.GET('/groups/{id}', { params: { path: { id } } })
    group = g.data ?? null
    members = (g.data?.members ?? []).map((m) => m.pull_request)
  }
  const current = list.data?.verdicts.find((v) => v.unit.kind === kind && v.unit.id === id)
  const verdict = current ? ((await api.GET('/verdicts/{id}', { params: { path: { id: current.id } } })).data ?? null) : null
  const got = await Promise.all(
    members.map(async (pr) => {
      const path = { params: { path: { id: pr } } }
      const [f, timeline] = await Promise.all([api.GET('/prs/{id}', path), api.GET('/prs/{id}/timeline', path)])
      return f.data ? [{ ...f.data, timeline: timeline.data?.events ?? [] }] : []
    }),
  )
  const tasks = await Promise.all(listed.map(async (x) => (await api.GET('/tasks/{id}', { params: { path: { id: x.id } } })).data ?? x))
  return { problem: list.error ? problem(list.error) : '', verdict, prs, tasks, group, facts: got.flat() }
}

// A unit: where its ball is and who holds it, every person's part with its path through the rules, the rule set
// version, each pull request's facts and the timeline of its fact events, and its tasks with their histories. A group
// can be renamed, and its pull requests taken out.
export default function Unit({ kind, id }: Props) {
  const [error, setError] = useState('')
  const { data, refresh } = useFetch(() => load(kind, id), `${kind}:${id}`)
  useLive(['verdict', 'task', 'pr', 'group'], refresh)

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

  if (!data) return <p className="quiet">Loading…</p>
  const { verdict, prs, tasks, group, facts } = data
  return (
    <section>
      <h1>{group ? group.name : prName(prs, id)}</h1>
      {(error || data.problem) && <p className="error">{error || data.problem}</p>}
      {group && (
        <form onSubmit={rename} className="inline">
          <input name="name" defaultValue={group.name} aria-label="Group name" />
          <button type="submit">Rename</button>
        </form>
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
          </h3>
          <p>
            Checks: {f.facts.checks.map((c) => `${c.name} ${c.state}`).join(', ') || 'none'}. Reviews:{' '}
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
                  <span className="quiet"> · by {e.actor}</span>
                </li>
              ))}
            </ol>
          </details>
        </article>
      ))}
    </section>
  )
}

// fieldsText names the fields a fact event changed, for reading.
function fieldsText(changes: Record<string, unknown>): string {
  return Object.keys(changes).sort().map(words).join(', ')
}

function login(f: Schemas['PullRequest'], account: number): string {
  return f.accounts.find((a) => a.id === account)?.login ?? String(account)
}
