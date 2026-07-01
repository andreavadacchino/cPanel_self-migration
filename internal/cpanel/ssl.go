package cpanel

import (
	"context"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

type SSLCertEntry struct {
	ID             string `json:"id"`
	FriendlyName   string `json:"friendly_name"`
	Domains        string `json:"domains"`
	IssuerCN       string `json:"issuer.commonName"`
	IssuerOrg      string `json:"issuer.organizationName"`
	NotBefore      int64  `json:"not_before"`
	NotAfter       int64  `json:"not_after"`
	IsSelfSigned   int    `json:"is_self_signed"`
	ValidationType string `json:"validation_type"`
	ModulusLength  int    `json:"modulus_length"`
}

func ListSSLCerts(ctx context.Context, c Runner) ([]SSLCertEntry, error) {
	data, err := RunUAPI[[]SSLCertEntry](ctx, c, "SSL", "list_certs", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].FriendlyName < data[j].FriendlyName })
	logx.Debug("ListSSLCerts: %d cert(s)", len(data))
	return data, nil
}
