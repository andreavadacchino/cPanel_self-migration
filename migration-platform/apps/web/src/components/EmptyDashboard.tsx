interface EmptyDashboardProps {
  onCreate?: () => void
}

export default function EmptyDashboard({ onCreate }: EmptyDashboardProps) {
  return (
    <div className="empty">
      <div className="empty__icon">📦</div>
      <p className="empty__title">Nessuna migrazione ancora</p>
      <p className="empty__text">
        Crea la tua prima migrazione per iniziare a spostare un account cPanel.
      </p>
      <button className="btn btn--primary" onClick={onCreate}>
        Nuova migrazione
      </button>
    </div>
  )
}
