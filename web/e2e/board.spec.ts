import { expect, test } from '@playwright/test'
import { signIn } from './session.ts'

// Ana's board: her units first — the Billing group, its pull requests each with her action, and her draft — then the
// unit she waits on, with who holds it, then the one she has no part in. She filters by the ball, folds the group,
// sorts by a column, both ways, and hides a column, which stays hidden after a reload.
test('the board shows every open unit as it stands for me', async ({ page }) => {
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Board' }).click()
  const rows = page.getByRole('table').locator('tbody tr')
  await expect(rows).toHaveCount(6)
  const row = (name: string) => rows.filter({ hasText: name })
  await expect(rows.first()).toContainText('Billing')
  await expect(rows.first().locator('.tag', { hasText: 'Yours' })).toBeVisible()
  await expect(row('api#12 Billing export')).toContainText('fix checks — 1 required checks failing')
  await expect(row('api#12 Billing export').locator('.tag', { hasText: 'Failing' })).toBeVisible()
  await expect(row('api#14 Retry failed payments')).toContainText('address comments')
  await expect(row('api#15 Export formats')).toContainText('hold — Draft')
  await expect(row('api#16 Statement emails').locator('.tag', { hasText: "Others'" })).toBeVisible()
  await expect(row('api#16 Statement emails')).toContainText('ben')
  await expect(row('api#13 Invoice layout').locator('.tag', { hasText: 'Not yours' })).toBeVisible()

  await page.getByRole('combobox', { name: 'Ball' }).selectOption('yours')
  await expect(rows).toHaveCount(4)
  await expect(page.getByText('2 of 4 units')).toBeVisible()
  await page.getByRole('combobox', { name: 'Ball' }).selectOption('')

  await page.getByRole('button', { name: 'Fold Billing' }).click()
  await expect(row('api#12 Billing export')).toHaveCount(0)
  await page.getByRole('button', { name: 'Unfold Billing' }).click()
  await expect(row('api#12 Billing export')).toBeVisible()

  const author = page.getByRole('button', { name: 'Author', exact: true })
  await author.click()
  await expect(page.locator('th[aria-sort="ascending"]')).toHaveText('Author ↑')
  await expect(rows.last()).toContainText('api#13')
  await author.click()
  await expect(page.locator('th[aria-sort="descending"]')).toHaveText('Author ↓')
  await expect(rows.first()).toContainText('api#13')

  await page.getByText('Columns').click()
  await page.getByRole('checkbox', { name: 'Author' }).uncheck()
  await expect(page.getByRole('button', { name: 'Author', exact: true })).toHaveCount(0)
  await page.reload()
  await expect(page.getByRole('table')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Author', exact: true })).toHaveCount(0)
  await page.getByText('Columns').click()
  await page.getByRole('checkbox', { name: 'Author' }).check()
  await expect(page.getByRole('button', { name: 'Author', exact: true })).toBeVisible()
})
