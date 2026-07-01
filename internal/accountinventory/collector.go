package accountinventory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
)

type HostInfo struct {
	User string
	Host string
}

type CollectResult struct {
	Source NormalizedInventory
	Dest   *NormalizedInventory
}

func Collect(ctx context.Context, src, dest cpanel.Runner, srcInfo, destInfo HostInfo) (CollectResult, error) {
	srcInv, err := collectSide(ctx, src, srcInfo, "source")
	if err != nil {
		return CollectResult{}, fmt.Errorf("source inventory: %w", err)
	}

	var result CollectResult
	result.Source = srcInv

	if dest != nil {
		destInv, err := collectSide(ctx, dest, destInfo, "destination")
		if err != nil {
			srcInv.Warnings = append(srcInv.Warnings, fmt.Sprintf("destination inventory failed: %v", err))
			result.Source = srcInv
			return result, nil
		}
		result.Dest = &destInv
	}

	return result, nil
}

func collectSide(ctx context.Context, r cpanel.Runner, info HostInfo, side string) (NormalizedInventory, error) {
	inv := NormalizedInventory{
		Account: AccountInfo{
			User:        info.User,
			Host:        info.Host,
			CollectedAt: time.Now().UTC().Format(time.RFC3339),
			Side:        side,
		},
		Domains:        []DomainEntry{},
		Mailboxes:      []MailboxEntry{},
		Databases:      []DatabaseEntry{},
		Forwarders:     []ForwarderEntry{},
		Autoresponders: []AutoresponderEntry{},
		Warnings:       []string{},
	}

	domains, err := cpanel.ListDomains(ctx, r)
	if err != nil {
		return inv, fmt.Errorf("list domains: %w", err)
	}
	for _, d := range domains {
		inv.Domains = append(inv.Domains, DomainEntry{
			Name: d.Name,
			Type: d.Type.String(),
		})
	}

	docroots, err := cpanel.ListDocroots(ctx, r)
	if err != nil {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf("docroots unavailable: %v", err))
	} else {
		docrootMap := make(map[string]string, len(docroots))
		for _, dr := range docroots {
			docrootMap[dr.Domain] = dr.DocumentRoot
		}
		for i := range inv.Domains {
			if root, ok := docrootMap[inv.Domains[i].Name]; ok {
				inv.Domains[i].DocumentRoot = root
			}
		}
	}

	accounts, err := cpanel.ListEmailAccounts(ctx, r)
	if err != nil {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf("Email accounts unavailable: %v", err))
	} else {
		for _, a := range accounts {
			local := a.Email
			domain := a.Domain
			user := local
			if at := strings.IndexByte(local, '@'); at >= 0 {
				user = local[:at]
			}
			inv.Mailboxes = append(inv.Mailboxes, MailboxEntry{
				Email:     a.Email,
				Domain:    domain,
				User:      user,
				DiskUsage: a.DiskUsedQuota,
			})
		}
	}

	dbs, err := cpanel.ListDatabases(ctx, r)
	if err != nil {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf("databases unavailable: %v", err))
	} else {
		for _, db := range dbs {
			inv.Databases = append(inv.Databases, DatabaseEntry{
				Name:      db.Database,
				DiskUsage: int64(db.DiskUsage),
				Users:     db.Users,
			})
		}
	}

	for _, d := range domains {
		fwds, err := cpanel.ListForwarders(ctx, r, d.Name)
		if err != nil {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf("forwarders for %s unavailable: %v", d.Name, err))
			continue
		}
		for _, f := range fwds {
			inv.Forwarders = append(inv.Forwarders, ForwarderEntry{
				Source:      f.Dest,
				Destination: f.Forward,
				Domain:      d.Name,
			})
		}

		ars, err := cpanel.ListAutoresponders(ctx, r, d.Name)
		if err != nil {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf("autoresponders for %s unavailable: %v", d.Name, err))
			continue
		}
		for _, a := range ars {
			inv.Autoresponders = append(inv.Autoresponders, AutoresponderEntry{
				Email:    a.Email + "@" + a.Domain,
				Domain:   a.Domain,
				Subject:  a.Subject,
				Interval: a.Interval,
			})
		}
	}

	inv.FTP = collectFTP(ctx, r)
	inv.SSL = collectSSL(ctx, r)
	inv.PHP = collectPHP(ctx, r)

	return inv, nil
}

func collectFTP(ctx context.Context, r cpanel.Runner) FTPSection {
	sec := FTPSection{
		ConfigSection: ConfigSection{Method: "uapi", SourceFunction: "Ftp::list_ftp_with_disk", Warnings: []string{}},
		Items:         []FTPEntry{},
	}
	accounts, err := cpanel.ListFTPAccounts(ctx, r)
	if err != nil {
		sec.Available = false
		sec.Warnings = append(sec.Warnings, fmt.Sprintf("FTP accounts unavailable: %v", err))
		return sec
	}
	sec.Available = true
	for _, a := range accounts {
		sec.Items = append(sec.Items, FTPEntry{
			Login:    a.Login,
			Type:     a.AcctType,
			Dir:      a.Dir,
			DiskUsed: a.DiskUsed,
		})
	}
	return sec
}

func collectSSL(ctx context.Context, r cpanel.Runner) SSLSection {
	sec := SSLSection{
		ConfigSection: ConfigSection{Method: "uapi", SourceFunction: "SSL::list_certs", Warnings: []string{}},
		Items:         []SSLEntry{},
	}
	certs, err := cpanel.ListSSLCerts(ctx, r)
	if err != nil {
		sec.Available = false
		sec.Warnings = append(sec.Warnings, fmt.Sprintf("SSL certificates unavailable: %v", err))
		return sec
	}
	sec.Available = true
	for _, c := range certs {
		sec.Items = append(sec.Items, SSLEntry{
			Domains:        c.Domains,
			Issuer:         c.IssuerCN,
			ValidFrom:      c.NotBefore,
			ValidUntil:     c.NotAfter,
			IsSelfSigned:   c.IsSelfSigned != 0,
			ValidationType: c.ValidationType,
		})
	}
	return sec
}

func collectPHP(ctx context.Context, r cpanel.Runner) PHPSection {
	sec := PHPSection{
		ConfigSection: ConfigSection{Method: "uapi", SourceFunction: "LangPHP::php_get_vhost_versions", Warnings: []string{}},
		Items:         []PHPEntry{},
	}
	versions, err := cpanel.ListPHPVersions(ctx, r)
	if err != nil {
		sec.Available = false
		sec.Warnings = append(sec.Warnings, fmt.Sprintf("PHP versions unavailable: %v", err))
		return sec
	}
	sec.Available = true
	for _, v := range versions {
		sec.Items = append(sec.Items, PHPEntry{
			Domain:  v.Vhost,
			Version: v.Version,
		})
	}
	return sec
}

