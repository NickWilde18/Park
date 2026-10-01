import { useState, type FormEvent } from 'react'
import { api, problem } from '../api/client'
import { useFetch } from '../fetch'
import { prName, unitPath, type PRs } from '../format'
import { Link } from '../Link'
import { useLive } from '../live'

// Groups with their pull requests, nested by chain and marked with the source that placed each, and the open pull
// requests in none; and the manual actions: create, move in, take out, merge, and split. A group is renamed on its
// own page.
export default function Groups() {
  const [error, setError] = useState('')
  const { data, refresh } = useFetch(async () => {
    const [g, p] = await Promise.all([api.GET('/groups'), api.GET('/prs')])
    const prs: PRs = new Map((p.data?.pull_requests ?? []).map((x) => [x.id, x]))
    return { groups: g.data?.groups, problem: g.error ? problem(g.error) : '', prs }
  })
  useLive(['group', 'pr'], refresh)
  const done = async (answer: Promise<{ error?: unknown }>) => {
    const { error } = await answer
    if (error) setError(problem(error))
    else {
      setError('')
      refresh()
    }
  }
  if (!data) return <p className="quiet">Loading…</p>
  const { groups, prs } = data
  if (!groups) return <p className="error">{data.problem}</p>
  const grouped = new Set(groups.flatMap((g) => g.members.map((m) => m.pull_request)))
  const loose = [...prs.values()].filter((p) => !grouped.has(p.id) && (p.state === 'open' || p.state === 'draft'))
  const picked = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const form = new FormData(e.currentTarget)
    return { form, prs: form.getAll('pr').map(Number) }
  }
  return (
    <section>
      <h1>Groups</h1>
      {error && <p className="error">{error}</p>}
      {groups.map((g) => (
        <article key={g.id} className="group">
          <h2>
            <Link to={unitPath({ kind: 'group', id: g.id })}>{g.name}</Link>
          </h2>
          <ul>
            {[...g.members]
              .sort((a, b) => (a.chain.id ?? a.pull_request) - (b.chain.id ?? b.pull_request) || a.chain.position - b.chain.position)
              .map((m) => (
                <li key={m.pull_request}>
                  {prName(prs, m.pull_request)} {prs.get(m.pull_request)?.title}
                  {m.chain.length > 1 && <span className="badge">{`${m.chain.position}/${m.chain.length}`}</span>}
                  <span className="quiet"> — {m.reason}</span>{' '}
                  <button onClick={() => void done(api.DELETE('/groups/{id}/members/{pr}', { params: { path: { id: g.id, pr: m.pull_request } } }))}>
                    Take out
                  </button>
                </li>
              ))}
          </ul>
          {loose.length > 0 && (
            <form
              className="inline"
              onSubmit={(e) => {
                const { form } = picked(e)
                void done(api.POST('/groups/{id}/members', { params: { path: { id: g.id } }, body: { pull_request: Number(form.get('into')) } }))
              }}
            >
              <select name="into" aria-label={`Move a pull request into ${g.name}`}>
                {loose.map((p) => (
                  <option key={p.id} value={p.id}>
                    {prName(prs, p.id)}
                  </option>
                ))}
              </select>
              <button type="submit">Move in</button>
            </form>
          )}
          {groups.length > 1 && (
            <form
              className="inline"
              onSubmit={(e) => {
                const { form } = picked(e)
                void done(api.POST('/groups/{id}/merge', { params: { path: { id: g.id } }, body: { from: Number(form.get('from')) } }))
              }}
            >
              <select name="from" aria-label={`Merge a group into ${g.name}`}>
                {groups
                  .filter((o) => o.id !== g.id)
                  .map((o) => (
                    <option key={o.id} value={o.id}>
                      {o.name}
                    </option>
                  ))}
              </select>
              <button type="submit">Merge into this</button>
            </form>
          )}
          <form
            className="inline"
            onSubmit={(e) => {
              const { form, prs: chosen } = picked(e)
              void done(api.POST('/groups/{id}/split', { params: { path: { id: g.id } }, body: { name: String(form.get('name')), pull_requests: chosen } }))
            }}
          >
            {g.members.map((m) => (
              <label key={m.pull_request}>
                <input type="checkbox" name="pr" value={m.pull_request} /> {prName(prs, m.pull_request)}
              </label>
            ))}
            <input name="name" placeholder="New group's name" aria-label={`Split ${g.name} into`} required />
            <button type="submit">Split</button>
          </form>
        </article>
      ))}
      <h2>New group</h2>
      <form
        onSubmit={(e) => {
          const { form, prs: chosen } = picked(e)
          void done(api.POST('/groups', { body: { name: String(form.get('name')), pull_requests: chosen } }))
        }}
      >
        {loose.map((p) => (
          <label key={p.id}>
            <input type="checkbox" name="pr" value={p.id} /> {prName(prs, p.id)} {p.title}
          </label>
        ))}
        <input name="name" placeholder="Name" aria-label="New group's name" required />
        <button type="submit">Create</button>
      </form>
    </section>
  )
}
