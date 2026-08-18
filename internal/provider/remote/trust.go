package remote

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	lxd_client "github.com/canonical/lxd/client"
	lxd_api "github.com/canonical/lxd/shared/api"
	incus_client "github.com/lxc/incus/v7/client"
	incus_api "github.com/lxc/incus/v7/shared/api"

	"github.com/aiyor/lxm/internal/provider"
)

// ServerInfo captures the remote server TLS certificate and fingerprint during discovery.
type ServerInfo struct {
	URL         string
	Fingerprint string
	CertPEM     string
	Provider    provider.ProviderType
}

// FetchServerCertificate connects to the remote HTTPS endpoint and retrieves its TLS server certificate.
func FetchServerCertificate(serverURL string) (*ServerInfo, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL %q: %w", serverURL, err)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		host = host + ":8443"
	}

	var peerCert *x509.Certificate
	conf := &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(rawCerts) > 0 {
				cert, err := x509.ParseCertificate(rawCerts[0])
				if err == nil {
					peerCert = cert
				}
			}
			return nil
		},
	}

	tr := &http.Transport{
		TLSClientConfig: conf,
		IdleConnTimeout: 5 * time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	targetURL := fmt.Sprintf("https://%s/1.0", host)
	resp, err := client.Get(targetURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if peerCert == nil {
		return nil, fmt.Errorf("no TLS certificate presented by server %s", host)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: peerCert.Raw,
	})

	fp := FingerprintSHA256(peerCert.Raw)

	return &ServerInfo{
		URL:         fmt.Sprintf("https://%s", host),
		Fingerprint: fp,
		CertPEM:     string(certPEM),
		Provider:    provider.ProviderTypeIncus, // default
	}, nil
}

// EnrollTrustTokenIncus uses a trust token to add the client mTLS certificate to an Incus server.
func EnrollTrustTokenIncus(serverURL, token, certPath, keyPath, serverCert string) error {
	clientCertPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("reading client certificate %q: %w", certPath, err)
	}
	clientKeyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("reading client key %q: %w", keyPath, err)
	}

	args := &incus_client.ConnectionArgs{
		TLSClientCert:      string(clientCertPEM),
		TLSClientKey:       string(clientKeyPEM),
		TLSServerCert:      serverCert,
		InsecureSkipVerify: serverCert == "",
	}

	c, err := incus_client.ConnectIncus(serverURL, args)
	if err != nil {
		return fmt.Errorf("connecting to Incus at %s: %w", serverURL, err)
	}

	req := incus_api.CertificatesPost{
		CertificatePut: incus_api.CertificatePut{
			Certificate: string(clientCertPEM),
			Type:        "client",
		},
		TrustToken: token,
	}

	return c.CreateCertificate(req)
}

// EnrollTrustTokenLXD uses a trust token to add the client mTLS certificate to an LXD server.
func EnrollTrustTokenLXD(serverURL, token, certPath, keyPath, serverCert string) error {
	clientCertPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("reading client certificate %q: %w", certPath, err)
	}
	clientKeyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("reading client key %q: %w", keyPath, err)
	}

	args := &lxd_client.ConnectionArgs{
		TLSClientCert:      string(clientCertPEM),
		TLSClientKey:       string(clientKeyPEM),
		TLSServerCert:      serverCert,
		InsecureSkipVerify: serverCert == "",
	}

	c, err := lxd_client.ConnectLXD(serverURL, args)
	if err != nil {
		return fmt.Errorf("connecting to LXD at %s: %w", serverURL, err)
	}

	req := lxd_api.CertificatesPost{
		Certificate: string(clientCertPEM),
		Type:        "client",
		TrustToken:  token,
	}

	return c.CreateCertificate(req)
}
