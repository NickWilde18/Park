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
