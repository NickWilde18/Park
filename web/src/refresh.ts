import { api, problem } from './api/client'

// refreshPRs asks Park to refresh each pull request from its forge now. It answers the first problem, or '' once
// every refresh is queued; the facts arrive later, through the event stream, if anything changed.
export async function refreshPRs(ids: number[]): Promise<string> {
  const answers = await Promise.all(ids.map((id) => api.POST('/prs/{id}/refresh', { params: { path: { id } } })))
  const failed = answers.find((a) => a.error)
  return failed?.error ? problem(failed.error) : ''
}

// asked says when the forge was asked about something.
export const asked = (what: string) => `Asked the forge about ${what} at ${new Date().toLocaleTimeString()}.`
