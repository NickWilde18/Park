import { useState, type FormEvent } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { useFetch } from '../fetch'
import { prName, unitPath, type PRs } from '../format'
import { Link } from '../Link'
import { useLive } from '../live'

type Answer = Promise<{ error?: unknown }>

// Groups with their pull requests, nested by chain and marked with the source that placed each, and the open pull
// requests in none; bridges' open proposals above them; and the manual actions: create, move in, take out, merge, and
// split. A group is renamed on its own page.
export default function Groups() {
  const [error, setError] = useState('')
  const { data, refresh } = useFetch(async () => {
    const [g, p, o] = await Promise.all([api.GET('/groups'), api.GET('/prs'), api.GET('/proposals')])
    const prs: PRs = new Map((p.data?.pull_requests ?? []).map((x) => [x.id, x]))
    return { groups: g.data?.groups, problem: g.error ? problem(g.error) : '', prs, proposals: o.data?.proposals ?? [] }
  })
  useLive(['group', 'pr', 'proposal'], refresh)
  const done = async (answer: Answer) => {
    const { error } = await answer
    if (error) setError(problem(error))
    else {
      setError('')
      refresh()
    }
  }
  if (!data) return <p className="quiet">Loading…</p>
  const { groups, prs, proposals } = data
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
      {proposals.length > 0 && <Proposed proposals={proposals} prs={prs} done={done} />}
      <section aria-label="Current groups">
        <h2>Current groups</h2>
        {groups.length === 0 && <p className="quiet">No groups yet.</p>}
        {groups.map((g) => (
          <article key={g.id} className="group">
            <h3>
              <Link to={unitPath({ kind: 'group', id: g.id })}>{g.name}</Link>
            </h3>
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
      </section>
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

// Proposed lists bridges' open proposals, each to accept — as proposed, or with its groups' names and pull requests
// edited — or to dismiss. Accepting unchanged lets Park name an unnamed group after its oldest pull request; an
// edited group left without a name takes the title of its first pull request still checked.
function Proposed({ proposals, prs, done }: { proposals: Schemas['Proposal'][]; prs: PRs; done: (answer: Answer) => Promise<void> }) {
  const accept = (p: Schemas['Proposal'], form: FormData) => {
    const groups = p.groups.map((_, i) => ({
      name: String(form.get(`name-${i}`) ?? '').trim(),
      pull_requests: form.getAll(`pr-${i}`).map(Number),
    }))
    const changed = groups.some((g, i) => g.name !== (p.groups[i].name ?? '') || g.pull_requests.length !== p.groups[i].pull_requests.length)
    const body = changed
      ? {
          groups: groups
            .filter((g) => g.pull_requests.length > 0)
            .map((g) => ({ ...g, name: g.name || prs.get(g.pull_requests[0])?.title || prName(prs, g.pull_requests[0]) })),
        }
      : {}
    return done(api.POST('/proposals/{id}/accept', { params: { path: { id: p.id } }, body }))
  }
  return (
    <section aria-label="Proposed">
      <h2>Proposed</h2>
      {proposals.map((p) => {
        const title = p.title ?? `A thread from ${p.bridge}`
        return (
          <article key={p.id} className="proposal">
            <h3>{p.link ? <a href={p.link}>{title}</a> : title}</h3>
            <p className="quiet">Proposed by {p.bridge}; nothing changes until it is accepted.</p>
            <form
              onSubmit={(e) => {
                e.preventDefault()
                void accept(p, new FormData(e.currentTarget))
              }}
            >
              {p.groups.map((g, i) => (
                <fieldset key={i}>
                  <input
                    name={`name-${i}`}
                    defaultValue={g.name ?? ''}
                    placeholder="Named after its oldest pull request"
                    aria-label={`Name of group ${i + 1} proposed in ${title}`}
                  />
                  {g.pull_requests.map((pr) => (
                    <label key={pr}>
                      <input type="checkbox" name={`pr-${i}`} value={pr} defaultChecked /> {prName(prs, pr)} {prs.get(pr)?.title}
                    </label>
                  ))}
                </fieldset>
              ))}
              <button type="submit" aria-label={`Accept ${title}`}>
                Accept
              </button>{' '}
              <button
                type="button"
                aria-label={`Dismiss ${title}`}
                onClick={() => void done(api.POST('/proposals/{id}/dismiss', { params: { path: { id: p.id } } }))}
              >
                Dismiss
              </button>
            </form>
          </article>
        )
      })}
    </section>
  )
}
