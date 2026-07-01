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

type AutoresponderEntry struct {
	Email    string `json:"email"`
	Domain   string `json:"domain"`
	Subject  string `json:"subject"`
	Interval int    `json:"interval"`
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

type NormalizedInventory struct {
	Account        AccountInfo          `json:"account"`
	Domains        []DomainEntry        `json:"domains"`
	Mailboxes      []MailboxEntry       `json:"mailboxes"`
	Databases      []DatabaseEntry      `json:"databases"`
	Forwarders     []ForwarderEntry     `json:"forwarders"`
	Autoresponders []AutoresponderEntry `json:"autoresponders"`
	FTP            FTPSection           `json:"ftp"`
	SSL            SSLSection           `json:"ssl"`
	PHP            PHPSection           `json:"php"`
	DNS            DNSSection           `json:"dns"`
	Cron           CronSection          `json:"cron"`
	Warnings       []string             `json:"warnings"`
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
		Warnings:       []string{},
	}
}
