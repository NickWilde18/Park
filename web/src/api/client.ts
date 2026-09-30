import createClient from 'openapi-fetch'
import type { paths } from './schema'

// The API client, typed from api/openapi.yaml. The base path is relative: the frontend is served by Park itself,
// and knows nothing about where Park runs.
export const api = createClient<paths>({ baseUrl: '/api/v1' })
