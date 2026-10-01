import createClient, { type Middleware } from 'openapi-fetch'
import type { components, paths } from './schema'

// The API client, typed from api/openapi.yaml. The base path is relative: the frontend is served by Park itself,
// and knows nothing about where Park runs.
export const api = createClient<paths>({ baseUrl: '/api/v1' })

let csrf = ''

// setCSRF keeps the session's CSRF token, which every request that changes state carries.
export function setCSRF(token: string) {
  csrf = token
}

const withCSRF: Middleware = {
  onRequest({ request }) {
    if (request.method !== 'GET' && request.method !== 'HEAD' && csrf) {
      request.headers.set('X-CSRF-Token', csrf)
    }
    return request
  },
}
api.use(withCSRF)

// problem is what an error answer says, for a person to read.
export function problem(error: unknown): string {
  const e = error as { error?: { message?: string } } | undefined
  return e?.error?.message ?? 'Park could not be reached.'
}

export type Schemas = components['schemas']
