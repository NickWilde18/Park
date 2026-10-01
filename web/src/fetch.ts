import { useEffect, useReducer, useRef, useState } from 'react'

// A run of fetches for one key: again is set when a refresh comes while one is under way.
type Flight = { key: string; again: boolean }

// useFetch runs fetch when the view appears, when key changes, and whenever refresh is called, and keeps its latest
// answer for the key it was fetched for: under a new key there is none until the first arrives, so a view never shows
// what it fetched for the key before. A refresh while a fetch is under way runs one more fetch once that one ends,
// instead of dropping it, so that a view whose fetches take longer than its changes take to come still shows each
// answer. A fetch that fails to reach Park is tried again after two seconds, then after twice as long each time, up
// to thirty; an answer that is an error is an answer.
export function useFetch<T>(fetch: () => Promise<T>, key = ''): { data: T | undefined; refresh: () => void } {
  const [answer, setAnswer] = useState<{ key: string; data: T }>()
  const [runs, refresh] = useReducer((n: number) => n + 1, 0)
  const latest = useRef(fetch)
  useEffect(() => {
    latest.current = fetch
  })
  const flight = useRef<Flight | null>(null)
  useEffect(() => {
    if (flight.current?.key === key) {
      flight.current.again = true
      return
    }
    const mine: Flight = { key, again: false }
    flight.current = mine
    let wait = 2000
    const run = () => {
      if (flight.current !== mine) return
      latest.current().then(
        (data) => {
          if (flight.current !== mine) return
          setAnswer({ key, data })
          wait = 2000
          if (mine.again) {
            mine.again = false
            run()
          } else {
            flight.current = null
          }
        },
        () => {
          if (flight.current !== mine) return
          setTimeout(run, wait)
          wait = Math.min(wait * 2, 30000)
        },
      )
    }
    run()
  }, [key, runs])
  useEffect(
    () => () => {
      flight.current = null
    },
    [],
  )
  return { data: answer?.key === key ? answer.data : undefined, refresh }
}
