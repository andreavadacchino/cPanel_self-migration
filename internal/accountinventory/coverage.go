package accountinventory

// PR 1A — coverage manifest. "What we do not see must declare itself": the
// checklist embeds a static registry of EVERY account area the tool knows
// about, each with its coverage state, so an uncollected area is a visible
// line in the operator's report instead of a silent absence.
//
// The manifest is purely DECLARATIVE. It never creates manual actions, never
// contributes to the summary counts and never changes the overall verdict —
// its whole job is to make the boundary of the tool's sight explicit.

// CoverageState classifies how the tool relates to an account area.
type CoverageState string

const (
	// CoverageCovered: the area is a real inventoried checklist section —
	// collected, diffed, policy-classified.
	CoverageCovered CoverageState = "covered"
	// CoverageRootOnly: the area cannot be read at all with account-level
	// access; it surfaces as an explicit root-only checklist section.
	CoverageRootOnly CoverageState = "root_only"
	// CoverageNotCollected: the area is account-accessible but the inventory
	// does not collect it (yet). Declared here so it can never be silently
	// absent; collectors are Fase 1B/1C work.
	CoverageNotCollected CoverageState = "not_collected"
)

// CoverageArea is one registry entry. Area names of covered/root_only entries
// are EXACTLY the checklist section names (pinned by tests); not_collected
// entries carry a note explaining what is at stake.
type CoverageArea struct {
	Area  string        `json:"area"`
	State CoverageState `json:"state"`
	Note  string        `json:"note,omitempty"`
}

// coverageRegistry is the single source of truth. Order: the checklist
// sections in their fixed order, then the not-collected areas alphabetically.
//
// KEEP IN LOCKSTEP with checklistSectionOrder (pinned by
// TestCoverageRegistryLockstepWithChecklistSections): a new inventoried
// section must flip its entry to covered here, and a new collector removes
// its area from the not_collected block.
var coverageRegistry = []CoverageArea{
	{Area: "domains", State: CoverageCovered},
	{Area: "web_files", State: CoverageCovered},
	{Area: "mailboxes", State: CoverageCovered},
	{Area: "databases", State: CoverageCovered},
	{Area: "forwarders", State: CoverageCovered},
	{Area: "autoresponders", State: CoverageCovered},
	{Area: "ftp", State: CoverageCovered},
	{Area: "ssl", State: CoverageCovered},
	{Area: "php", State: CoverageCovered},
	{Area: "dns", State: CoverageCovered},
	{Area: "cron", State: CoverageCovered},
	{Area: "email_routing", State: CoverageCovered},
	{Area: "default_address", State: CoverageCovered},
	{Area: "email_filters", State: CoverageCovered},
	{Area: "redirects", State: CoverageCovered},
	{Area: "quota_package", State: CoverageRootOnly,
		Note: "package assignment, quotas and bandwidth limits are WHM territory"},
	{Area: "server_level_config", State: CoverageRootOnly,
		Note: "PHP handlers, web server, firewall and system crons are not visible with account-level access"},

	{Area: "api_tokens", State: CoverageNotCollected,
		Note: "API token NAMES are listable user-level; secrets are never retrievable — historical-dossier material"},
	// autoresponder_bodies: collected since PR 2B-2 (get_auto_responder per
	// listed address) — folded into the covered "autoresponders" section.
	{Area: "boxtrapper", State: CoverageNotCollected,
		Note: "BoxTrapper enable state and configuration"},
	{Area: "contact_info", State: CoverageNotCollected,
		Note: "account contact addresses and notification preferences"},
	{Area: "directory_privacy", State: CoverageNotCollected,
		Note: "password-protected directories (~/.htpasswds) — the protection passwords are at stake on transfer"},
	{Area: "domain_aliases", State: CoverageNotCollected,
		Note: "parked/alias domains as a dedicated field — today folded into the domains listing"},
	{Area: "email_filter_rules", State: CoverageNotCollected,
		Note: "full filter RULES (round-trippable, Fase 2B prerequisite) — today only per-account/per-mailbox counts are collected"},
	{Area: "git_repositories", State: CoverageNotCollected,
		Note: "cPanel-registered git repositories (working trees travel with the home transfer, the registrations do not)"},
	{Area: "hotlink_protection", State: CoverageNotCollected,
		Note: "hotlink protection configuration"},
	{Area: "leech_protection", State: CoverageNotCollected,
		Note: "leech protection configuration"},
	{Area: "mailbox_quota_limits", State: CoverageNotCollected,
		Note: "per-mailbox quota LIMITS — usage is collected, the configured limit is not"},
	{Area: "mailing_lists", State: CoverageNotCollected,
		Note: "Mailman mailing lists (member rosters are root-only and cannot be migrated user-level)"},
	{Area: "mime_handlers", State: CoverageNotCollected,
		Note: "custom MIME types and Apache handlers"},
	{Area: "passenger_apps", State: CoverageNotCollected,
		Note: "registered Passenger/Node/Python applications — files travel with the transfer, the registrations do not"},
	{Area: "spamassassin", State: CoverageNotCollected,
		Note: "SpamAssassin enable state and user_prefs (~/.spamassassin is outside the docroot copy)"},
	{Area: "ssh_keys", State: CoverageNotCollected,
		Note: "SSH key METADATA (names/fingerprints only — private keys are never collected)"},
	{Area: "team_users", State: CoverageNotCollected,
		Note: "cPanel Team user accounts (their passwords cannot be migrated)"},
	{Area: "webdisk_accounts", State: CoverageNotCollected,
		Note: "WebDisk accounts (passwords would need regeneration on the destination)"},
}

// CoverageAreas returns the full manifest, as a copy: callers can embed or
// mutate the result without corrupting the registry.
func CoverageAreas() []CoverageArea {
	out := make([]CoverageArea, len(coverageRegistry))
	copy(out, coverageRegistry)
	return out
}
