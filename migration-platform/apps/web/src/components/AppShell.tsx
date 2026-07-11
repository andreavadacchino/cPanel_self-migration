import { useState, type ReactNode } from 'react'
import Sidebar from './Sidebar'
import Topbar from './Topbar'

interface AppShellProps {
  children: ReactNode
}

export default function AppShell({ children }: AppShellProps) {
  const [sidebarOpen, setSidebarOpen] = useState(false)

  return (
    <div className="app-shell">
      <Sidebar open={sidebarOpen} onClose={() => setSidebarOpen(false)} />
      {sidebarOpen && (
        <button
          aria-label="Chiudi navigazione"
          className="shell-backdrop"
          onClick={() => setSidebarOpen(false)}
          type="button"
        />
      )}
      <div className="shell-main">
        <Topbar onMenuToggle={() => setSidebarOpen((open) => !open)} />
        <main className="content">{children}</main>
      </div>
    </div>
  )
}
