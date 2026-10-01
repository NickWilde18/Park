import { expect, test, type Page } from '@playwright/test'
import { signIn } from './session.ts'

// call calls the API from the page, as the dashboard does, with the session's CSRF header. The session cookie is
// Secure, which the browser sends to a local address over plain HTTP, and Playwright's own requests do not.
async function call(page: Page, method: string, path: string, body?: unknown): Promise<{ status: number; body: unknown }> {
  return page.evaluate(
    async ({ method, path, body }) => {
      const me = (await (await fetch('/api/v1/me')).json()) as { csrf_token: string }
      const headers = { 'Content-Type': 'application/json', 'X-CSRF-Token': me.csrf_token }
      const r = await fetch(`/api/v1${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
      return { status: r.status, body: (await r.json()) as unknown }
    },
    { method, path, body },
  )
}

type Automation = { actions: string[]; repos?: number[] }

// Ana limits her automation to one repository through the API, then turns one more action on from the settings page:
// the saved automation has the new action and is still limited to that repository.
test('saving automation keeps the repositories it is limited to', async ({ page }) => {
  await signIn(page, 'ana')
  const prs = (await call(page, 'GET', '/prs')).body as { pull_requests: { repo: { id: number } }[] }
  const repo = prs.pull_requests[0].repo.id
  const limited = await call(page, 'PUT', '/me/automation', { actions: ['fix_checks', 'address_comments'], repos: [repo] })
  expect(limited.status).toBe(200)

  await page.getByRole('link', { name: 'Settings' }).click()
  await page.getByLabel('review', { exact: true }).check()
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText('Automation saved.')).toBeVisible()
  const saved = (await call(page, 'GET', '/me/automation')).body as Automation
  expect([...saved.actions].sort()).toEqual(['address_comments', 'fix_checks', 'review'])
  expect(saved.repos).toEqual([repo])
})
