// Package certs records the TLS certificates that HTTPS checks see, and warns before they expire.
//
// Certificates are stored only when something about them changes, and alerts go out from a
// worker of their own, so neither adds to the cost of a check or waits on a slow provider.
package certs

import (
	"crypto/sha1" //nolint:gosec // Only shown as the certificate's fingerprint, which browsers still do.
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
)

// Cert is one certificate, with the field names the web UI reads. The Node.js backend passed on
// what Node's getPeerCertificate returned, so some fields are in that module's style.
type Cert struct {
	Subject           map[string]string `json:"subject"`
	Issuer            map[string]string `json:"issuer"`
	SubjectAltName    string            `json:"subjectaltname,omitempty"`
	ValidFrom         time.Time         `json:"valid_from"`
	ValidTo           time.Time         `json:"validTo"`
	ValidFor          []string          `json:"validFor,omitempty"`
	SerialNumber      string            `json:"serialNumber"`
	Fingerprint       string            `json:"fingerprint"`
	Fingerprint256    string            `json:"fingerprint256"`
	DaysRemaining     int               `json:"daysRemaining"`
	CertType          string            `json:"certType"`
	IssuerCertificate *Cert             `json:"issuerCertificate"`

	// trustedRoot marks a root from the system's trust store. Replacing those isn't up to the
	// site's owner, so they don't alert.
	trustedRoot bool
}

// Info is what the UI shows about a monitor's certificate.
type Info struct {
	Valid                   bool  `json:"valid"`
	CertInfo                *Cert `json:"certInfo"`
	HostnameMatchMonitorURL bool  `json:"hostnameMatchMonitorUrl"`
}

// Read describes a connection's certificates, or returns nil if it has none. rawURL is the
// monitor's URL, whose host the first certificate should match.
func Read(state *tls.ConnectionState, rawURL string, now time.Time) *Info {
	if state == nil || len(state.PeerCertificates) == 0 {
		return nil
	}
	leaf := state.PeerCertificates[0]
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Hostname()
	}

	// A transport that skips verification has no verified chains, and the UI still says whether
	// the certificate is valid, so it's verified here.
	chain := state.PeerCertificates
	verified := state.VerifiedChains
	if len(verified) == 0 {
		intermediates := x509.NewCertPool()
		for _, c := range state.PeerCertificates[1:] {
			intermediates.AddCert(c)
		}
		// An error leaves verified empty, which means not valid.
		verified, _ = leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: intermediates, CurrentTime: now})
	}
	if len(verified) > 0 {
		chain = verified[0]
	}

	return &Info{
		Valid:                   len(verified) > 0,
		CertInfo:                chainInfo(chain, len(verified) > 0, now),
		HostnameMatchMonitorURL: host == "" || leaf.VerifyHostname(host) == nil,
	}
}

func chainInfo(chain []*x509.Certificate, verified bool, now time.Time) *Cert {
	var first, previous *Cert
	for i, c := range chain {
		// A root signs itself, which also ends a chain the server sent in a loop.
		last := i == len(chain)-1 || c.Equal(chain[i+1])
		info := certInfo(c, now)
		switch {
		case last && i == 0:
			info.CertType = "self-signed"
		case last:
			info.CertType = "root CA"
			info.trustedRoot = verified
		case i == 0:
			info.CertType = "server"
		default:
			info.CertType = "intermediate CA"
		}
		if previous == nil {
			first = info
		} else {
			previous.IssuerCertificate = info
		}
		previous = info
		if last {
			break
		}
	}
	return first
}

func certInfo(c *x509.Certificate, now time.Time) *Cert {
	sum1 := sha1.Sum(c.Raw) //nolint:gosec // See the import.
	sum256 := sha256.Sum256(c.Raw)
	var names, altNames []string
	for _, name := range c.DNSNames {
		names = append(names, name)
		altNames = append(altNames, "DNS:"+name)
	}
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
		altNames = append(altNames, "IP Address:"+ip.String())
	}
	return &Cert{
		Subject:        nameMap(c.Subject),
		Issuer:         nameMap(c.Issuer),
		SubjectAltName: strings.Join(altNames, ", "),
		ValidFrom:      c.NotBefore.UTC(),
		ValidTo:        c.NotAfter.UTC(),
		ValidFor:       names,
		SerialNumber:   strings.ToUpper(c.SerialNumber.Text(16)),
		Fingerprint:    colons(sum1[:]),
		Fingerprint256: colons(sum256[:]),
		DaysRemaining:  int(c.NotAfter.Sub(now).Hours() / 24),
		CertType:       "",
	}
}

func nameMap(name pkix.Name) map[string]string {
	out := map[string]string{}
	set := func(key string, values []string) {
		if len(values) > 0 {
			out[key] = strings.Join(values, ", ")
		}
	}
	set("C", name.Country)
	set("ST", name.Province)
	set("L", name.Locality)
	set("O", name.Organization)
	set("OU", name.OrganizationalUnit)
	if name.CommonName != "" {
		out["CN"] = name.CommonName
	}
	return out
}

// colons formats a fingerprint as Node does: upper case hex pairs.
func colons(sum []byte) string {
	text := strings.ToUpper(hex.EncodeToString(sum))
	var b strings.Builder
	for i := 0; i < len(text); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(text[i : i+2])
	}
	return b.String()
}
