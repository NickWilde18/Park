import type { Schemas } from './api/client'

// since says roughly how long ago a time was: "< 1 min", "5 min", "3 h", "2 d".
export function since(at: string, now = Date.now()): string {
  const minutes = Math.max(0, Math.round((now - Date.parse(at)) / 60000))
  if (minutes < 1) return '< 1 min'
  if (minutes < 60) return `${minutes} min`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `${hours} h`
  return `${Math.round(hours / 24)} d`
}

// problemText says what stops collection through an installation on the forge named: the forge refusing its
// credential, and for how long; or its rate limit, and when it resets.
export function problemText(p: { problem: Schemas['CollectionProblemKind']; since: string; until?: string }, forge: string): string {
  if (p.problem === 'rate_limited') return `${forge} is limiting Park's calls${p.until ? ` until ${new Date(p.until).toLocaleTimeString()}` : ''}`
  return `${forge} has refused Park's credential for ${since(p.since)}`
}

export type PRs = Map<number, Schemas['PullRequestSummary']>

// prName names a pull request as repo#number.
export function prName(prs: PRs, id: number): string {
  const p = prs.get(id)
  return p ? `${p.repo.name}#${p.number}` : `pull request ${id}`
}

// checkStates names where a pull request's required checks stand on its latest commit, and says what each name
// covers, as the board's CI column and a unit's page show them.
export const checkStates = {
  failing: { text: 'Failed', hint: 'A required check on the latest commit failed.' },
  pending: {
    text: 'Running',
    hint: 'No required check failed, and one is still queued, running, or not reported on the latest commit yet.',
  },
  passing: { text: 'Passed', hint: 'Every required check on the latest commit finished, and passed.' },
  none: { text: 'None', hint: 'The base branch requires no checks.' },
} as const

// unitPath is the dashboard's page of a unit.
export function unitPath(unit: Schemas['Unit']): string {
  return `/units/${unit.kind === 'pull_request' ? 'pr' : 'group'}/${unit.id}`
}

// reasonsOf is what a task event gives as its reasons, such as a failed verification's, item by item.
export function reasonsOf(e: Schemas['TaskEvent']): string[] {
  const reasons: unknown = e.detail.reasons
  return Array.isArray(reasons) ? reasons.map(String) : []
}

// attemptFailures lists each failed verification's reasons with the attempt they were for, from a task's events in
// order: each claim begins an attempt.
export function attemptFailures(events: Schemas['TaskEvent'][]): { attempt: number; reason: string }[] {
  let attempt = 0
  return events.flatMap((e) => {
    if (e.kind === 'claimed') attempt++
    return e.kind === 'verification_failed' ? reasonsOf(e).map((reason) => ({ attempt, reason })) : []
  })
}

// words writes an identifier for reading: request_review as "request review".
export function words(s?: string): string {
  return (s ?? '').replaceAll('_', ' ')
}

// roundFailures say why a bridge's round failed, for the reasons a person can act on; others are read as words.
const roundFailures: Record<string, string> = {
  signin_lapsed: 'its account has to be connected again',
  plugin_failed: "its plugin failed, and Park's log says why",
}

// roundText says how a bridge Park runs did in its last round — how long ago, what it read and changed, or why it
// failed — and whether the bridge is late.
export function roundText(b: { last_round?: Schemas['BridgeRound']; late: boolean }): string {
  const r = b.last_round
  if (!r) return 'No round yet.'
  const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`
  const said =
    r.outcome === 'ok'
      ? `Last round ${since(r.ended_at)} ago: read ${plural(r.read, 'message')}, ${plural(r.changes, 'change')}.`
      : `Last round ${since(r.ended_at)} ago failed: ${roundFailures[r.reason] ?? words(r.reason)}.`
  return b.late ? `${said} It is late: no round has ended in two periods.` : said
}

// pathText writes a part's path through its role's rules: the role, then each rule tried — its passed-over label when
// it did not match, nothing when it has none — and the rule that decided. Rules never reached do not appear. A rule
// that reads a judgement nobody has answered is marked as unknown says, such as a preview's "would ask the model".
export function pathText(part: Schemas['VerdictPart'], unknown = 'judgement not known'): string {
  const steps: string[] = [part.role]
  for (const s of part.path) {
    if (s.outcome === 'matched') steps.push(s.name)
    else if (s.outcome === 'unknown') steps.push(`${s.name} · ${unknown}`)
    else if (s.outcome === 'error') steps.push(`${s.name} · failed`)
    else if (s.passed_over) steps.push(s.passed_over)
  }
  return steps.join(' → ')
}

const operators: Record<string, string> = {
  is_true: '', is_false: 'not', eq: '=', ne: '≠', lt: '<', le: '≤', gt: '>', ge: '≥', is: 'is', is_not: 'is not',
  is_one_of: 'is one of', starts_with: 'starts with', contains: 'contains', is_empty: 'is empty',
  is_not_empty: 'is not empty', is_set: 'is set', is_not_set: 'is not set',
}

function valueText(v: unknown): string {
  if (v && typeof v === 'object' && 'field' in v) return String((v as { field: unknown }).field)
  if (Array.isArray(v)) return v.join(', ')
  return String(v)
}

// conditionText writes a rule's condition for reading, as the rule tables do.
export function conditionText(c: Schemas['Condition']): string {
  if (c.always) return 'always'
  if (c.expr) return c.expr
  if (c.all) return c.all.map(conditionText).join(', and ')
  if (c.any) return c.any.map(conditionText).join(', or ')
  const op = operators[c.op ?? ''] ?? c.op ?? ''
  if (c.op === 'is_true') return c.field ?? ''
  if (c.op === 'is_false') return `not ${c.field}`
  return [c.field, op, c.value === undefined ? '' : valueText(c.value)].filter(Boolean).join(' ')
}
