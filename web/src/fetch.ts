import { useEffect, useReducer, useRef, useState } from 'react'

// useFetch runs fetch when the view appears, when key changes, and whenever refresh is called, and keeps its latest
// answer. An answer to an earlier run that arrives after a later one began is dropped.
export function useFetch<T>(fetch: () => Promise<T>, key = ''): { data: T | undefined; refresh: () => void } {
  const [data, setData] = useState<T>()
  const [runs, refresh] = useReducer((n: number) => n + 1, 0)
  const latest = useRef(fetch)
  useEffect(() => {
    latest.current = fetch
  })
  useEffect(() => {
    let dropped = false
    void latest.current().then((answer) => {
      if (!dropped) setData(answer)
    })
    return () => {
      dropped = true
    }
  }, [key, runs])
  return { data, refresh }
}
