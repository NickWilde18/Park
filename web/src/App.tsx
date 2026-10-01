import type { ReactNode } from 'react'
import { api } from './api/client'
import Board from './pages/Board'
import Groups from './pages/Groups'
import Queue from './pages/Queue'
import RuleDraft from './pages/RuleDraft'
import Rules from './pages/Rules'
import Settings from './pages/Settings'
import Tasks from './pages/Tasks'
import Unit from './pages/Unit'
import { Link } from './Link'
import { usePath } from './router'
import { SessionGate } from './session'
import { useSession } from './sessionContext'

export default function App() {
  return (
    <SessionGate>
      <Shell />
    </SessionGate>
  )
}

function page(path: string): ReactNode {
  const unit = /^\/units\/(group|pr)\/(\d+)$/.exec(path)
  if (unit) return <Unit kind={unit[1] === 'pr' ? 'pull_request' : 'group'} id={Number(unit[2])} />
  const draft = /^\/rules\/drafts\/(\d+)$/.exec(path)
  if (draft) return <RuleDraft id={Number(draft[1])} />
  switch (path) {
    case '/':
      return <Queue />
    case '/board':
      return <Board />
    case '/tasks':
      return <Tasks />
    case '/groups':
      return <Groups />
    case '/rules':
      return <Rules />
    case '/settings':
      return <Settings />
  }
  return <p>There is no such page.</p>
}

function Shell() {
  const path = usePath()
  const { me } = useSession()
  const signOut = async () => {
    await api.POST('/auth/logout')
    window.location.assign('/')
  }
  return (
    <>
      <header>
        <nav>
          <Link to="/">Queue</Link>
          <Link to="/board">Board</Link>
          <Link to="/tasks">Tasks</Link>
          <Link to="/groups">Groups</Link>
          <Link to="/rules">Rules</Link>
          <Link to="/settings">Settings</Link>
        </nav>
        <span className="who">
          {me.member.display_name} <button onClick={signOut}>Sign out</button>
        </span>
      </header>
      <main className={path === '/board' ? 'wide' : undefined}>{page(path)}</main>
    </>
  )
}
