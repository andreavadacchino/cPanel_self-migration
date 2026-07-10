import type { ReactNode } from 'react'
import Sidebar from './Sidebar'
import Topbar from './Topbar'

interface AppShellProps {
  children: ReactNode
}

export default function AppShell({ children }: AppShellProps) {
  return (
    <div className="app-shell">
      <Sidebar />
      <div className="app-shell__main">
        <Topbar />
        <main className="content">{children}</main>
      </div>
    </div>
  )
}
