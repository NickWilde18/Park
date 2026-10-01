import { expect, test } from '@playwright/test'
import { call, signIn } from './session.ts'

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

// Unlinking an account from Settings. Park's refusal of a last way in is shown as Park words it; an unlink Park allows
// is said, and the list asked for again. Park's answers are given here, so that the seeded links stay.
test('settings unlink an account, unless it is the last way in', async ({ page }) => {
  const refusal = 'You sign in with this account, and have no other way in; connect another account first.'
  const answers = [{ status: 409, json: { error: { code: 'last_sign_in', message: refusal } } }, { status: 204 }]
  let unlinks = 0
  await page.route(/\/api\/v1\/me\/identities\/\d+$/, async (route) => {
    expect(route.request().method()).toBe('DELETE')
    await route.fulfill(answers[unlinks++])
  })
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Settings' }).click()
  const unlink = page.getByRole('button', { name: 'Unlink ana on Hub' })
  await unlink.click()
  await expect(page.locator('.notice')).toHaveText(refusal)
  const listed = page.waitForRequest((r) => r.method() === 'GET' && new URL(r.url()).pathname === '/api/v1/me/identities')
  await unlink.click()
  await expect(page.locator('.notice')).toHaveText('Account unlinked.')
  await listed
  expect(unlinks).toBe(2)
})
