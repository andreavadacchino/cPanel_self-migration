import { NavLink } from 'react-router-dom'

interface SidebarProps {
  open: boolean
  onClose: () => void
}

export default function Sidebar({ open, onClose }: SidebarProps) {
  return (
    <aside className={`sidebar ${open ? 'sidebar--open' : ''}`}>
      <div className="sidebar__brand">
        <span className="sidebar__logo">M</span>
        <div>
          <span>Migration Platform</span>
          <p className="sidebar__tag">cPanel migration ops</p>
        </div>
      </div>
      <nav className="sidebar__nav">
        <NavLink
          className={({ isActive }) =>
            `sidebar__item ${isActive ? 'sidebar__item--active' : ''}`
          }
          onClick={onClose}
          to="/"
        >
          <span className="sidebar__item-label">Migrazioni</span>
          <span className="sidebar__item-meta">Sessioni e stato</span>
        </NavLink>
        <div className="sidebar__item sidebar__item--ghost">
          <span className="sidebar__item-label">Job</span>
          <span className="sidebar__item-meta">Preflight e attività</span>
        </div>
        <div className="sidebar__item sidebar__item--ghost">
          <span className="sidebar__item-label">Endpoint</span>
          <span className="sidebar__item-meta">Sorgente e destinazione</span>
        </div>
        <div className="sidebar__item sidebar__item--ghost">
          <span className="sidebar__item-label">Impostazioni</span>
          <span className="sidebar__item-meta">Workspace locale</span>
        </div>
      </nav>
      <div className="sidebar__footer">
        <div className="sidebar__pulse" />
        <div>
          <div className="sidebar__footer-label">Operatore locale</div>
          <div className="sidebar__footer-meta">Runtime collegato</div>
        </div>
      </div>
    </aside>
  )
}
