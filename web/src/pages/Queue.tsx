import { useState } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { useFetch } from '../fetch'
import { prName, since, unitPath, words, type PRs } from '../format'
import { Link } from '../Link'
import { useLive } from '../live'
import { useSession } from '../sessionContext'

type Verdict = Schemas['Verdict']
type Part = Schemas['HeldPart']

// My queue: every unit whose ball I hold, with my actions on each pull request in chain order, oldest hold first
// within a priority; then the units whose ball someone else holds where I have work too, with that work; the reminders
// offered to me; and, collapsed, every unit I have a part in whose ball someone else holds.
export default function Queue() {
  const { accounts } = useSession()
  const [error, setError] = useState('')
  const { data, refresh } = useFetch(async () => {
    const [q, p, t, g] = await Promise.all([api.GET('/queue'), api.GET('/prs'), api.GET('/tasks'), api.GET('/groups')])
    const prs: PRs = new Map((p.data?.pull_requests ?? []).map((x) => [x.id, x]))
    const groups = g.data?.groups ?? []
    const names = new Map(groups.map((x) => [x.id, x.name]))
    const chains = new Map(groups.flatMap((x) => x.members.map((m) => [m.pull_request, m.chain] as const)))
    return { queue: q.data, problem: q.error ? problem(q.error) : '', prs, tasks: t.data?.tasks ?? [], names, chains }
  })
  useLive(['verdict', 'task', 'pr', 'group'], refresh)
  if (!data) return <p className="quiet">Loading…</p>
  const { queue, prs, tasks, names, chains } = data
  if (!queue) return <p className="error">{data.problem}</p>

  const mine = (v: Verdict) => v.holders.find((h) => accounts.has(h.account.id))
  const low = (v: Verdict) => (mine(v)?.parts ?? []).every((p) => p.priority === 'low')
  const holding = [...queue.holding].sort(
    (a, b) => Number(low(a)) - Number(low(b)) || Date.parse(mine(a)?.since ?? '') - Date.parse(mine(b)?.since ?? ''),
  )
  const name = (v: Verdict) => (v.unit.kind === 'group' ? (names.get(v.unit.id) ?? `Group ${v.unit.id}`) : prName(prs, v.unit.id))
  // A unit's parts go chain by chain, each chain from its bottom up.
  const inChainOrder = <T extends { pull_request: number }>(parts: T[]) =>
    [...parts].sort((a, b) => {
      const x = chains.get(a.pull_request)
      const y = chains.get(b.pull_request)
      return (x?.id ?? a.pull_request) - (y?.id ?? b.pull_request) || (x?.position ?? 0) - (y?.position ?? 0)
    })
  // A unit whose ball someone else holds can still have work of mine in it: a review asked of me while the author works,
  // say. The ball stays where it is; the work is listed so that it is not missed.
  const also = queue.waiting
    .map((v) => ({ v, parts: inChainOrder(v.parts.filter((p) => accounts.has(p.account.id) && p.kind === 'act')) }))
    .filter(({ parts }) => parts.length > 0)
  // One part of mine: the pull request, where it sits in its chain, and what I am to do there, and why.
  const partItem = (p: Part) => {
    const chain = chains.get(p.pull_request)
    return (
      <li key={`${p.pull_request}-${p.kind}`}>
        <a href={prs.get(p.pull_request)?.url}>{prName(prs, p.pull_request)}</a>
        {chain && chain.length > 1 && <span className="badge">{`${chain.position}/${chain.length}`}</span>}
        {prs.get(p.pull_request)?.stale && <span className="badge stale">stale</span>}{' '}
        <strong>{words(p.action ?? p.kind)}</strong>
        {p.priority === 'low' && <span className="badge">low</span>} — {p.reason}
      </li>
    )
  }
  const taskOf = (v: Verdict) =>
    tasks.find((t) => t.unit.kind === v.unit.kind && t.unit.id === v.unit.id && !['done', 'cancelled', 'dismissed'].includes(t.state))
  const reminders = [...queue.holding, ...queue.waiting].flatMap((v) =>
    v.reminders.filter((r) => accounts.has(r.account.id)).map((r) => ({ v, r })),
  )
  const nudge = async (pr: number) => {
    const { error } = await api.POST('/prs/{id}/nudged', { params: { path: { id: pr } } })
    if (error) setError(problem(error))
    else refresh()
  }

  return (
    <section>
      <h1>My queue</h1>
      {error && <p className="error">{error}</p>}
      {holding.length === 0 && <p className="quiet">Nothing is waiting on you.</p>}
      <ul className="units" aria-label="Holding">
        {holding.map((v) => {
          const h = mine(v)
          const task = taskOf(v)
          return (
            <li key={v.id} className="unit">
              <Link to={unitPath(v.unit)}>{name(v)}</Link>
              <span className="badge">held {h ? since(h.since) : ''}</span>
              {task && (
                <Link to={`/tasks#task-${task.id}`}>
                  <span className="badge task">task {words(task.state)}</span>
                </Link>
              )}
              <ul>{inChainOrder(h?.parts ?? []).map(partItem)}</ul>
            </li>
          )
        })}
      </ul>
      {also.length > 0 && (
        <>
          <h2>Also yours to do</h2>
          <p className="quiet">Someone else holds the ball on these, but you have work on them too.</p>
          <ul className="units" aria-label="Also yours">
            {also.map(({ v, parts }) => (
              <li key={v.id} className="unit">
                <Link to={unitPath(v.unit)}>{name(v)}</Link>
                <span className="quiet"> held by {v.holders.map((h) => h.account.login).join(', ')}</span>
                <ul>{parts.map(partItem)}</ul>
              </li>
            ))}
          </ul>
        </>
      )}
      {reminders.length > 0 && (
        <>
          <h2>Reminders</h2>
          <ul>
            {reminders.map(({ v, r }) => (
              <li key={`${v.id}-${r.pull_request}`}>
                {prName(prs, r.pull_request)}: {r.reason} <button onClick={() => void nudge(r.pull_request)}>Mark nudged</button>
              </li>
            ))}
          </ul>
        </>
      )}
      <details>
        <summary>Waiting on others ({queue.waiting.length})</summary>
        <ul className="units" aria-label="Waiting">
          {queue.waiting.map((v) => (
            <li key={v.id}>
              <Link to={unitPath(v.unit)}>{name(v)}</Link> held by{' '}
              {v.holders.map((h) => `${h.account.login} (${h.parts.map((p) => p.reason).join('; ')})`).join(', ')}
            </li>
          ))}
        </ul>
      </details>
    </section>
  )
}
