package model

// Action is what must happen on the destination for a source domain.
type Action int

const (
	// AlreadyPresent: the domain already exists on the destination.
	AlreadyPresent Action = iota
	// CreateAddon: create a real addon domain (api2 AddonDomain::addaddondomain).
	CreateAddon
	// CreateSub: create a subdomain (uapi SubDomain::addsubdomain).
	CreateSub
)

// ActionFor maps a source domain type + destination presence to the action
// needed on the destination:
//
//	main / addon / parked -> addon  (the dest main domain is fixed)
//	sub                   -> subdomain
//	already present       -> nothing
func ActionFor(src DomainType, existsOnDest bool) Action {
	if existsOnDest {
		return AlreadyPresent
	}
	switch src {
	case Sub:
		return CreateSub
	default: // Main, Addon, Parked
		return CreateAddon
	}
}

// ExpectedDestinationType is the domain type this migration expects to find or
// create on the destination for a source domain. The destination account's main
// domain is fixed, so source main/addon/parked domains migrate as addons; source
// subdomains remain subdomains.
func ExpectedDestinationType(src DomainType) DomainType {
	if src == Sub {
		return Sub
	}
	return Addon
}

// CompatibleDestinationType reports whether an existing destination domain has
// the type this migration expects for the source domain.
func CompatibleDestinationType(src, dest DomainType) bool {
	return dest == ExpectedDestinationType(src)
}

// HashScheme maps a crypt(3) hash prefix to a human-readable scheme name (used
// by the analysis report). Only the prefix is inspected; the full hash is never
// needed here.
func HashScheme(hash string) string {
	switch {
	case hash == "":
		return "EMPTY"
	case has(hash, "$6$"):
		return "SHA-512"
	case has(hash, "$5$"):
		return "SHA-256"
	case has(hash, "$2"):
		return "bcrypt"
	case has(hash, "$1$"):
		return "MD5 (weak)"
	case has(hash, "$y$"):
		return "yescrypt"
	case has(hash, "$argon2"):
		return "Argon2"
	case hash[0] == '!' || hash[0] == '*':
		return "LOCKED/none"
	default:
		return "unknown"
	}
}

func has(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
