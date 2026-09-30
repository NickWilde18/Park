import { useEffect, useState } from 'react'
import { api } from './api/client'

// The only page so far: it shows that the frontend reaches the API through the generated client.
export default function App() {
  const [status, setStatus] = useState('Asking Park…')

  useEffect(() => {
    const controller = new AbortController()
    api
      .GET('/auth/providers', { signal: controller.signal })
      .then(({ data, error }) => {
        if (error) {
          setStatus(`Park answered with an error: ${error.error.code}.`)
        } else if (data.providers.length === 0) {
          setStatus('No sign-in providers are configured yet.')
        } else {
          setStatus(`${data.providers.length} sign-in providers are configured.`)
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setStatus('Park could not be reached.')
      })
    return () => controller.abort()
  }, [])

  return (
    <main>
      <h1>Park</h1>
      <p>{status}</p>
    </main>
  )
}
