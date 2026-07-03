// Package accountinventory collects a read-only inventory of a cPanel account:
// domains, docroots, mailboxes, databases, and DNS zones.
// It never writes to any server.
package accountinventory

import (
	"encoding/json"
	"time"
)

type AccountInfo struct {
	User        string `json:"user"`
	Host        string `json:"host"`
	CollectedAt string `json:"collected_at"`
	Side        string `json:"side"`
}

type DomainEntry struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	DocumentRoot string `json:"document_root,omitempty"`
}

type MailboxEntry struct {
	Email     string `json:"email"`
	Domain    string `json:"domain"`
	User      string `json:"user"`
	DiskUsage int64  `json:"disk_usage,omitempty"`
}

type DatabaseEntry struct {
	Name      string   `json:"name"`
	DiskUsage int64    `json:"disk_usage,omitempty"`
	Users     []string `json:"users"`
}

type ForwarderEntry struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Domain      string `json:"domain"`
}

// AutoresponderEntry is one autoresponder with its full content (PR 2B-2).
// Email is always the full local@domain address; Domain is the QUERIED
// domain (real list_auto_responders rows carry no domain field). The
// content fields (From, Body, IsHTML, Interval, Start, Stop, Charset) come
// from get_auto_responder and are only trustworthy when BodyCollected is
// true: a false value means the entry carries list-level facts only
// (pre-2B-2 artifact, or the per-address get failed — see Warnings) and no
// equality over the content can be proven.
type AutoresponderEntry struct {
	Email    string `json:"email"`
	Domain   string `json:"domain"`
	Subject  string `json:"subject"`
	Interval int    `json:"interval"`
	From     string `json:"from,omitempty"`
	Body     string `json:"body,omitempty"`
	IsHTML   int    `json:"is_html,omitempty"`
	Start    int64  `json:"start,omitempty"`
	Stop     int64  `json:"stop,omitempty"`
	Charset  string `json:"charset,omitempty"`
	// BodyCollected reports that get_auto_responder succeeded for this
	// address (2B-2 body collector). It is the honesty marker the email
	// plan gates on before proving autoresponder equality.
	BodyCollected bool `json:"body_collected,omitempty"`
}

type FTPEntry struct {
	Login    string `json:"login"`
	Type     string `json:"type"`
	Dir      string `json:"dir"`
	DiskUsed int64  `json:"disk_used"`
}

type SSLEntry struct {
	Domains        string `json:"domains"`
	Issuer         string `json:"issuer"`
	ValidFrom      int64  `json:"valid_from"`
	ValidUntil     int64  `json:"valid_until"`
	IsSelfSigned   bool   `json:"is_self_signed"`
	ValidationType string `json:"validation_type"`
}

type PHPEntry struct {
	Domain  string `json:"domain"`
	Version string `json:"version"`
}

type ConfigSection struct {
	Available      bool     `json:"available"`
	Method         string   `json:"method"`
	SourceFunction string   `json:"source_function"`
	Warnings       []string `json:"warnings"`
}

type FTPSection struct {
	ConfigSection
	Items []FTPEntry `json:"items"`
}

type SSLSection struct {
	ConfigSection
	Items []SSLEntry `json:"items"`
}

type PHPSection struct {
	ConfigSection
	Items []PHPEntry `json:"items"`
}

type DNSRecordEntry struct {
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	TTL      int             `json:"ttl"`
	Value    string          `json:"value"`
	Priority int             `json:"priority,omitempty"`
	Exchange string          `json:"exchange,omitempty"`
	Address  string          `json:"address,omitempty"`
	Target   string          `json:"target,omitempty"`
	TxtData  string          `json:"txtdata,omitempty"`
	Class    string          `json:"class,omitempty"`
	Line     int             `json:"line,omitempty"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

type DNSZoneResult struct {
	Available      bool             `json:"available"`
	Zone           string           `json:"zone"`
	Method         string           `json:"method"`
	SourceFunction string           `json:"source_function"`
	Records        []DNSRecordEntry `json:"records"`
	Warnings       []string         `json:"warnings"`
	Errors         []string         `json:"errors"`
	RawIncluded    bool             `json:"raw_included"`
}

type DNSSection struct {
	ConfigSection
	Zones []DNSZoneResult `json:"zones"`
}

type CronJobEntry struct {
	Type            string   `json:"type"`
	Minute          string   `json:"minute,omitempty"`
	Hour            string   `json:"hour,omitempty"`
	DayOfMonth      string   `json:"day_of_month,omitempty"`
	Month           string   `json:"month,omitempty"`
	DayOfWeek       string   `json:"day_of_week,omitempty"`
	Macro           string   `json:"macro,omitempty"`
	CommandRedacted string   `json:"command_redacted"`
	CommandSHA256   string   `json:"command_sha256"`
	RawLineSHA256   string   `json:"raw_line_sha256"`
	Enabled         bool     `json:"enabled"`
	LineNumber      int      `json:"line_number"`
	Warnings        []string `json:"warnings"`
}

type CronEnvEntry struct {
	Name          string `json:"name"`
	ValueRedacted string `json:"value_redacted"`
	LineNumber    int    `json:"line_number"`
}

// CronSection deviates from ConfigSection on purpose: crontab is fetched by
// a shell command, not a cPanel API function, so it carries source_command
// instead of source_function.
type CronSection struct {
	Available         bool           `json:"available"`
	Method            string         `json:"method"` // "ssh_crontab_l" | "unavailable"
	SourceCommand     string         `json:"source_command"`
	Jobs              []CronJobEntry `json:"jobs"`
	Environment       []CronEnvEntry `json:"environment"`
	CommentsCount     int            `json:"comments_count"`
	DisabledJobsCount int            `json:"disabled_jobs_count"`
	Warnings          []string       `json:"warnings"`
	Errors            []string       `json:"errors"`
}

// MXRecordEntry is one MX record row of an email-routing domain.
type MXRecordEntry struct {
	Priority int64  `json:"priority"`
	Exchange string `json:"exchange"`
}

// EmailRoutingEntry is one domain's mail-routing mode (PR 7E). Only
// mail-routing domains appear here — subdomains have a default address
// but no routing entry of their own, so this section's domain universe
// is narrower than DefaultAddresses'.
type EmailRoutingEntry struct {
	Domain       string          `json:"domain"`
	Routing      string          `json:"routing"`  // configured mxcheck: local | remote | auto | secondary
	Detected     string          `json:"detected"` // what cPanel detects from the MX records
	AlwaysAccept bool            `json:"always_accept"`
	MXRecords    []MXRecordEntry `json:"mx_records"`
}

// DefaultAddressEntry is one domain's catch-all configuration (PR 7E);
// the value is opaque (the cPanel default embeds literal quotes).
type DefaultAddressEntry struct {
	Domain         string `json:"domain"`
	DefaultAddress string `json:"default_address"`
}

// FilterRule is one rule of an email filter (2B-3-pre fact 1–3). The
// opt field is always null in all observed responses but is retained for
// completeness. match_type (AND/OR join between rules) is NOT returned
// by list_filters or get_filter — see 2B-3-pre fact 10.
type FilterRule struct {
	Part  string `json:"part"`
	Match string `json:"match"`
	Opt   any    `json:"opt"`
	Val   string `json:"val"`
}

// FilterAction is one action of an email filter. Dest is null (nil
// pointer) for actions that have no destination (fail, finish).
type FilterAction struct {
	Action string  `json:"action"`
	Dest   *string `json:"dest"`
}

// EmailFilterEntry is one email filter (PR 7E, extended in 2B-3). Since
// the user decision (2B-3 gate: option A), rules and actions are stored
// in clear in the inventory for round-trip fidelity. RulesCollected is
// the honesty marker: true means get_filter succeeded and the Rules/
// Actions slices are trustworthy; false means the entry carries
// list-level facts only (pre-2B-3 artifact, or a per-filter get failed)
// and no equality over the content can be proven. RuleCount/ActionCount
// are kept for backward compatibility with pre-2B-3 artifacts.
//
// ⚠️ match_type (AND/OR join between rules) is NOT round-trippable:
// the cPanel API does not return it (2B-3-pre fact 10). Single-rule
// filters are safe (match_type irrelevant); multi-rule filters must be
// classified MANUAL by the plan.
type EmailFilterEntry struct {
	Account     string `json:"account"` // "" = account-level (all mail)
	FilterName  string `json:"filter_name"`
	Enabled     bool   `json:"enabled"`
	RuleCount   int    `json:"rule_count"`
	ActionCount int    `json:"action_count"`
	// Rules and Actions carry the full filter content (2B-3, option A).
	// Populated when RulesCollected is true.
	Rules          []FilterRule   `json:"rules,omitempty"`
	Actions        []FilterAction `json:"actions,omitempty"`
	RulesCollected bool           `json:"rules_collected,omitempty"`
}

// RedirectEntry is one redirect/rewrite harvested from .htaccess by
// cPanel (PR 7E). Raw facts only — the CMS-noise classification
// (rewrite+temporary+no status code) belongs to the policy layer.
type RedirectEntry struct {
	Domain      string `json:"domain"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Kind        string `json:"kind"`
	Type        string `json:"type"`
	StatusCode  int64  `json:"status_code"` // 0 = none reported
	Wildcard    bool   `json:"wildcard"`
	MatchWWW    bool   `json:"match_www"`
}

type EmailRoutingSection struct {
	ConfigSection
	Items []EmailRoutingEntry `json:"items"`
}

type DefaultAddressSection struct {
	ConfigSection
	Items []DefaultAddressEntry `json:"items"`
}

type EmailFilterSection struct {
	ConfigSection
	Items []EmailFilterEntry `json:"items"`
}

type RedirectSection struct {
	ConfigSection
	Items []RedirectEntry `json:"items"`
}

type NormalizedInventory struct {
	Account          AccountInfo           `json:"account"`
	Domains          []DomainEntry         `json:"domains"`
	Mailboxes        []MailboxEntry        `json:"mailboxes"`
	Databases        []DatabaseEntry       `json:"databases"`
	Forwarders       []ForwarderEntry      `json:"forwarders"`
	Autoresponders   []AutoresponderEntry  `json:"autoresponders"`
	FTP              FTPSection            `json:"ftp"`
	SSL              SSLSection            `json:"ssl"`
	PHP              PHPSection            `json:"php"`
	DNS              DNSSection            `json:"dns"`
	Cron             CronSection           `json:"cron"`
	EmailRouting     EmailRoutingSection   `json:"email_routing"`
	DefaultAddresses DefaultAddressSection `json:"default_address"`
	EmailFilters     EmailFilterSection    `json:"email_filters"`
	Redirects        RedirectSection       `json:"redirects"`
	Warnings         []string              `json:"warnings"`
}

// NewEmptyCronSection returns a CronSection with every slice initialized so
// JSON output never contains null arrays.
func NewEmptyCronSection() CronSection {
	return CronSection{
		SourceCommand: "crontab -l",
		Jobs:          []CronJobEntry{},
		Environment:   []CronEnvEntry{},
		Warnings:      []string{},
		Errors:        []string{},
	}
}

func NewEmptyInventory(user, host, side string) NormalizedInventory {
	return NormalizedInventory{
		Account: AccountInfo{
			User:        user,
			Host:        host,
			CollectedAt: time.Now().UTC().Format(time.RFC3339),
			Side:        side,
		},
		Domains:        []DomainEntry{},
		Mailboxes:      []MailboxEntry{},
		Databases:      []DatabaseEntry{},
		Forwarders:     []ForwarderEntry{},
		Autoresponders: []AutoresponderEntry{},
		FTP:            FTPSection{ConfigSection: ConfigSection{Warnings: []string{}}, Items: []FTPEntry{}},
		SSL:            SSLSection{ConfigSection: ConfigSection{Warnings: []string{}}, Items: []SSLEntry{}},
		PHP:            PHPSection{ConfigSection: ConfigSection{Warnings: []string{}}, Items: []PHPEntry{}},
		DNS:            DNSSection{ConfigSection: ConfigSection{Warnings: []string{}}, Zones: []DNSZoneResult{}},
		Cron:           NewEmptyCronSection(),
		EmailRouting: EmailRoutingSection{
			ConfigSection: ConfigSection{Warnings: []string{}}, Items: []EmailRoutingEntry{}},
		DefaultAddresses: DefaultAddressSection{
			ConfigSection: ConfigSection{Warnings: []string{}}, Items: []DefaultAddressEntry{}},
		EmailFilters: EmailFilterSection{
			ConfigSection: ConfigSection{Warnings: []string{}}, Items: []EmailFilterEntry{}},
		Redirects: RedirectSection{
			ConfigSection: ConfigSection{Warnings: []string{}}, Items: []RedirectEntry{}},
		Warnings: []string{},
	}
}
