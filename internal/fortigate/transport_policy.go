package fortigate

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kgskr/fortigate-external-dns/internal/config"
)

var errRedirectNotAllowed = errors.New("FortiGate API redirects are not allowed")

// transportPolicy is the credential-bearing FortiGate client's mandatory
// transport boundary. It independently enforces HTTPS, TLS trust, and a
// reject-all redirect policy before any request can carry the bearer token.
type transportPolicy struct {
	transport     *http.Transport
	checkRedirect func(*http.Request, []*http.Request) error
}

func newTransportPolicy(cfg config.FortiGateConfig) (*transportPolicy, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return nil, errors.New("FortiGate URL scheme must be https")
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec
	}
	if strings.TrimSpace(cfg.CAFile) != "" || len(cfg.CAData) != 0 {
		// The CA bundle replaces (not extends) the system roots: the FortiGate
		// device is this client's only peer, so trusting anything beyond its
		// issuing chain only widens the attack surface. Validate has already
		// confirmed the file reads and parses; a race with file removal here still
		// fails closed.
		data := cfg.CAData
		if len(data) == 0 {
			data, err = os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read FortiGate CA file: %w", err)
			}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, errors.New("FortiGate CA bundle contains no PEM certificates")
		}
		tlsConfig.RootCAs = pool
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &transportPolicy{
		transport: transport,
		checkRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errRedirectNotAllowed
		},
	}, nil
}

func (p *transportPolicy) client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     p.transport,
		CheckRedirect: p.checkRedirect,
	}
}
