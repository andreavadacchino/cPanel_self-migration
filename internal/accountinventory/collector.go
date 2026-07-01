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
	inv.DNS = collectDNS(ctx, r, inv.Domains)
	inv.Cron = collectCron(ctx, r)

	return inv, nil
}

func collectCron(ctx context.Context, r cpanel.Runner) CronSection {
	sec := NewEmptyCronSection()

	res, err := cpanel.FetchCrontab(ctx, r)
	if err != nil {
		sec.Available = false
		sec.Method = "unavailable"
		// Hard failures land in Errors; Warnings stays for soft conditions
		// (empty crontab, unparsable lines) so JSON consumers can key off
		// a non-empty errors array.
		sec.Errors = append(sec.Errors, fmt.Sprintf("crontab unavailable: %v", err))
		return sec
	}

	sec.Available = true
	sec.Method = "ssh_crontab_l"
	sec.CommentsCount = res.CommentsCount
	sec.DisabledJobsCount = res.DisabledJobsCount
	sec.Warnings = append(sec.Warnings, res.Warnings...)
	for _, j := range res.Jobs {
		warnings := j.Warnings
		if warnings == nil {
			warnings = []string{}
		}
		sec.Jobs = append(sec.Jobs, CronJobEntry{
			Type:            j.Type,
			Minute:          j.Minute,
			Hour:            j.Hour,
			DayOfMonth:      j.DayOfMonth,
			Month:           j.Month,
			DayOfWeek:       j.DayOfWeek,
			Macro:           j.Macro,
			CommandRedacted: j.CommandRedacted,
			CommandSHA256:   j.CommandSHA256,
			RawLineSHA256:   j.RawLineSHA256,
			Enabled:         j.Enabled,
			LineNumber:      j.LineNumber,
			Warnings:        warnings,
		})
	}
	for _, e := range res.Environment {
		sec.Environment = append(sec.Environment, CronEnvEntry{
			Name:          e.Name,
			ValueRedacted: e.ValueRedacted,
			LineNumber:    e.LineNumber,
		})
	}
	return sec
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
			DiskUsed: int64(a.DiskUsed),
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
			Domains:        string(c.Domains),
			Issuer:         c.IssuerCN,
			ValidFrom:      c.NotBefore,
			ValidUntil:     c.NotAfter,
			IsSelfSigned:   c.IsSelfSigned != 0,
			ValidationType: c.ValidationType,
		})
	}
	return sec
}

func collectDNS(ctx context.Context, r cpanel.Runner, domains []DomainEntry) DNSSection {
	sec := DNSSection{
		ConfigSection: ConfigSection{Warnings: []string{}},
		Zones:         []DNSZoneResult{},
	}

	seen := map[string]bool{}
	for _, d := range domains {
		if d.Type == "sub" {
			continue
		}
		zone := d.Name
		if seen[zone] {
			continue
		}
		seen[zone] = true

		zr := DNSZoneResult{
			Zone:     zone,
			Records:  []DNSRecordEntry{},
			Warnings: []string{},
			Errors:   []string{},
		}

		records, err := cpanel.FetchDNSZoneUAPI(ctx, r, zone)
		if err == nil {
			zr.Available = true
			zr.Method = "uapi"
			zr.SourceFunction = "DNS::parse_zone"
			zr.Records = toDNSRecordEntries(records)
			zr.RawIncluded = hasRawRecords(records)
			sec.Zones = append(sec.Zones, zr)
			continue
		}

		records, err = cpanel.FetchDNSZoneAPI2(ctx, r, zone)
		if err == nil {
			zr.Available = true
			zr.Method = "api2"
			zr.SourceFunction = "ZoneEdit::fetchzone_records"
			zr.Records = toDNSRecordEntries(records)
			zr.RawIncluded = hasRawRecords(records)
		} else {
			zr.Available = false
			zr.Method = "unavailable"
			zr.Warnings = append(zr.Warnings, fmt.Sprintf("DNS zone %s unavailable: %v", zone, err))
		}

		sec.Zones = append(sec.Zones, zr)
	}

	anyAvailable := false
	for _, z := range sec.Zones {
		if z.Available {
			anyAvailable = true
			break
		}
	}
	sec.Available = anyAvailable
	if anyAvailable {
		for _, z := range sec.Zones {
			if z.Available {
				sec.Method = z.Method
				sec.SourceFunction = z.SourceFunction
				break
			}
		}
	} else {
		sec.Method = "unavailable"
	}

	return sec
}

func toDNSRecordEntries(records []cpanel.DNSRecord) []DNSRecordEntry {
	out := make([]DNSRecordEntry, 0, len(records))
	for _, r := range records {
		out = append(out, DNSRecordEntry{
			Type:     r.Type,
			Name:     r.Name,
			TTL:      r.TTL,
			Value:    r.Value,
			Priority: r.Priority,
			Exchange: r.Exchange,
			Address:  r.Address,
			Target:   r.Target,
			TxtData:  r.TxtData,
			Class:    r.Class,
			Line:     r.Line,
			Raw:      r.Raw,
		})
	}
	return out
}

func hasRawRecords(records []cpanel.DNSRecord) bool {
	for _, r := range records {
		if r.Raw != nil {
			return true
		}
	}
	return false
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

