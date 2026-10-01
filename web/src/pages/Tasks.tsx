import { useEffect, useState, type FormEvent } from 'react'
import { api, problem } from '../api/client'
import { useFetch } from '../fetch'
import { unitPath, words } from '../format'
import { Link } from '../Link'
import { useLive } from '../live'

const order = ['needs_person', 'leased', 'verifying', 'queued', 'done', 'cancelled', 'dismissed']

// Tasks by state, the inbox of those that need a person first: every item's failures, and retry and dismiss. Opened
// at #task-N, the page scrolls to that task once the tasks are in.
export default function Tasks() {
  const [error, setError] = useState('')
  const { data, refresh } = useFetch(async () => {
    const { data, error } = await api.GET('/tasks')
    return { tasks: data?.tasks, problem: error ? problem(error) : '' }
  })
  useLive(['task'], refresh)
  const loaded = data !== undefined
  useEffect(() => {
    if (loaded && window.location.hash) document.getElementById(window.location.hash.slice(1))?.scrollIntoView()
  }, [loaded])
  const retry = async (id: number) => {
    const { error } = await api.POST('/tasks/{id}/retry', { params: { path: { id } } })
    if (error) setError(problem(error))
    else refresh()
  }
  const dismiss = async (e: FormEvent<HTMLFormElement>, id: number) => {
    e.preventDefault()
    const reason = String(new FormData(e.currentTarget).get('reason') ?? '')
    const { error } = await api.POST('/tasks/{id}/dismiss', { params: { path: { id } }, body: { reason } })
    if (error) setError(problem(error))
    else refresh()
  }
  if (!data) return <p className="quiet">Loading…</p>
  const tasks = data.tasks
  if (!tasks) return <p className="error">{data.problem}</p>
  return (
    <section>
      <h1>Tasks</h1>
      {error && <p className="error">{error}</p>}
      {tasks.length === 0 && <p className="quiet">No tasks: turn automation on in Settings.</p>}
      {order.map((state) => {
        const these = tasks.filter((t) => t.state === state)
        if (these.length === 0) return null
        return (
          <div key={state}>
            <h2>
              {state === 'needs_person' ? 'Needs a person' : words(state)} ({these.length})
            </h2>
            <ul className="tasks">
              {these.map((t) => (
                <li key={t.id} id={`task-${t.id}`}>
                  <Link to={unitPath(t.unit)}>Task {t.id}</Link>, attempt {t.attempt}
                  {t.executor && `, ${t.executor}`}
                  {t.cancel_marked && <span className="badge">marked to cancel</span>}
                  {t.note && <span className="quiet"> — {t.note}</span>}
                  {t.reason && <span className="quiet"> — {t.reason}</span>}
                  <ul>
                    {t.items.map((i) => (
                      <li key={i.id}>
                        {words(i.action)} ({i.state}){i.failures.length > 0 && `: ${i.failures.join('; ')}`}
                      </li>
                    ))}
                  </ul>
                  {state === 'needs_person' && <button onClick={() => void retry(t.id)}>Retry</button>}
                  {['queued', 'leased', 'needs_person'].includes(state) && (
                    <form onSubmit={(e) => void dismiss(e, t.id)} className="inline">
                      <input name="reason" placeholder="Why" aria-label={`Why dismiss task ${t.id}`} required />
                      <button type="submit">Dismiss</button>
                    </form>
                  )}
                </li>
              ))}
            </ul>
          </div>
        )
      })}
    </section>
  )
}
