// Package accountinventory collects a read-only inventory of a cPanel account:
// domains, docroots, mailboxes, and databases. It never writes to any server.
package accountinventory

import "time"

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

type NormalizedInventory struct {
	Account   AccountInfo     `json:"account"`
	Domains   []DomainEntry   `json:"domains"`
	Mailboxes []MailboxEntry  `json:"mailboxes"`
	Databases []DatabaseEntry `json:"databases"`
	Warnings  []string        `json:"warnings"`
}

func NewEmptyInventory(user, host, side string) NormalizedInventory {
	return NormalizedInventory{
		Account: AccountInfo{
			User:        user,
			Host:        host,
			CollectedAt: time.Now().UTC().Format(time.RFC3339),
			Side:        side,
		},
		Domains:   []DomainEntry{},
		Mailboxes: []MailboxEntry{},
		Databases: []DatabaseEntry{},
		Warnings:  []string{},
	}
}
