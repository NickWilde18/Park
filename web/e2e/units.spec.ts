import { expect, test } from '@playwright/test'
import { call, signIn } from './session.ts'

// These tests change the groups, so they run after the others: files run in order of name.

// Ana's pull request 11 merged yesterday: its page names it, and says how its unit closed.
test('a closed unit shows how it closed', async ({ page }) => {
  await signIn(page, 'ana')
  await page.goto('/units/pr/1')
  await expect(page.getByRole('heading', { name: 'api#11', exact: true })).toBeVisible()
  await expect(page.getByText('Closed out: merged')).toBeVisible()
})

// Ana groups Carol's pull request on its own, then merges that group into the first: the merged group's page is the
// page of the group it joined, with that group's verdict.
test('a merged group\'s page is the page of the group it joined', async ({ page }) => {
  await signIn(page, 'ana')
  const made = (await call(page, 'POST', '/groups', { name: 'Invoices', pull_requests: [3] })).body as { id: number }
  expect((await call(page, 'POST', '/groups/1/merge', { from: made.id })).status).toBe(200)
  await page.goto(`/units/group/${made.id}`)
  await expect(page).toHaveURL(/\/units\/group\/1$/)
  await expect(page.locator('table.parts')).toBeVisible()
  await expect(page.getByText('No verdict yet.')).toHaveCount(0)
})

// Taking a pull request out of its group opens the pull request's own page. While that page loads, slowed here, it
// shows nothing of the group's: no name, no rename.
test('a unit\'s page shows nothing of the unit before it', async ({ page }) => {
  await signIn(page, 'ana')
  await page.goto('/units/group/1')
  await expect(page.locator('table.parts')).toBeVisible()
  await page.route('**/api/v1/prs/4', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 3000))
    await route.continue()
  })
  await page.getByRole('button', { name: 'Take api#14 out of the group' }).click()
  await expect(page).toHaveURL(/\/units\/pr\/4$/)
  await expect(page.getByText('Loading…')).toBeVisible()
  await expect(page.getByLabel('Group name')).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'api#14', exact: true })).toBeVisible({ timeout: 10_000 })
})
