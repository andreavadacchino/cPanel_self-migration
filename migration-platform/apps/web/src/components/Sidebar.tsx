export default function Sidebar() {
  return (
    <aside className="sidebar">
      <div className="sidebar__brand">
        <span className="sidebar__logo">M</span>
        <span>Migration Platform</span>
      </div>
      <nav className="sidebar__nav">
        <div className="sidebar__item sidebar__item--active">Migrazioni</div>
        <div className="sidebar__item">Job</div>
        <div className="sidebar__item">Endpoint</div>
        <div className="sidebar__item">Impostazioni</div>
      </nav>
    </aside>
  )
}
