import { expect, type Page } from '@playwright/test'

// The password cmd/seed gives its members.
const password = 'a long enough password'

// signIn signs a seeded member in on the sign-in page, and waits for their queue.
export async function signIn(page: Page, username: string) {
  await page.goto('/')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'My queue' })).toBeVisible()
}

// call calls the API from the page, as the dashboard does, with the session's CSRF header. The session cookie is
// Secure, which the browser sends to a local address over plain HTTP, and Playwright's own requests do not.
export async function call(page: Page, method: string, path: string, body?: unknown): Promise<{ status: number; body: unknown }> {
  return page.evaluate(
    async ({ method, path, body }) => {
      const me = (await (await fetch('/api/v1/me')).json()) as { csrf_token: string }
      const headers = { 'Content-Type': 'application/json', 'X-CSRF-Token': me.csrf_token }
      const r = await fetch(`/api/v1${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
      return { status: r.status, body: r.status === 204 ? null : ((await r.json()) as unknown) }
    },
    { method, path, body },
  )
}
