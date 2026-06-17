// Package model holds the core domain/mailbox types and the pure mapping
// logic shared across the migration phases.
package model

// DomainType is a domain's configuration type on a cPanel account.
type DomainType int

const (
	Main DomainType = iota
	Addon
	Sub
	Parked
)

// String returns the lowercase name used in the migration plan/report
// ("type on source: <t>").
func (t DomainType) String() string {
	switch t {
	case Main:
		return "main"
	case Addon:
		return "addon"
	case Sub:
		return "sub"
	case Parked:
		return "parked"
	default:
		return "unknown"
	}
}

// Domain is a configured domain with its source type.
type Domain struct {
	Name string
	Type DomainType
}

// Mailbox is one email account on a domain.
type Mailbox struct {
	Domain string
	User   string
	Hash   string // crypt hash from the source shadow; "" if none found
	Scheme string // human-readable password scheme, for the analysis report
	Active bool   // listed in ~/etc/<dom>/passwd (vs ORPHAN mail dir)
}

// Email returns "user@domain".
func (m Mailbox) Email() string { return m.User + "@" + m.Domain }
