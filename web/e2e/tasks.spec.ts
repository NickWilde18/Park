import { expect, test } from '@playwright/test'
import { signIn } from './session.ts'

// Ben's executor tried his review three times, and verification found no review each time: the inbox shows the task
// with each attempt's failures, not only the last one's, and lets him retry or dismiss it.
test('the inbox shows every attempt\'s failures', async ({ page }) => {
  await signIn(page, 'ben')
  await page.getByRole('link', { name: 'Tasks' }).click()
  await expect(page.getByRole('heading', { name: 'Needs a person (1)' })).toBeVisible()
  const task = page.locator('#task-2')
  const attempts = task.getByRole('list', { name: 'Every attempt\'s failures' })
  for (const n of [1, 2, 3]) {
    await expect(attempts.getByText(`Attempt ${n}: review: no review was submitted`)).toBeVisible()
  }
  await expect(task.getByRole('button', { name: 'Retry' })).toBeVisible()
  await expect(task.getByRole('button', { name: 'Dismiss' })).toBeVisible()
})
