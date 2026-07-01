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
	Warnings       []string             `json:"warnings"`
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
		Warnings:       []string{},
	}
}
