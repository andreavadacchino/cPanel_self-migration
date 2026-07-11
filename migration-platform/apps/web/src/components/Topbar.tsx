import { useLocation } from 'react-router-dom'

interface TopbarProps {
  onMenuToggle: () => void
}

export default function Topbar({ onMenuToggle }: TopbarProps) {
  const location = useLocation()
  const inDetail = location.pathname.startsWith('/migrations/')

  return (
    <header className="topbar">
      <div className="topbar__lead">
        <button
          aria-label="Apri navigazione"
          className="topbar__menu"
          onClick={onMenuToggle}
          type="button"
        >
          <span />
          <span />
          <span />
        </button>
        <div>
          <div className="topbar__eyebrow">cPanel migration ops</div>
          <div className="topbar__title">
            {inDetail ? 'Setup migrazione' : 'Migrazioni'}
          </div>
        </div>
      </div>
      <div className="topbar__actions">
        <div className="topbar__signal">Runtime attivo</div>
        <div className="avatar">OP</div>
      </div>
    </header>
  )
}
