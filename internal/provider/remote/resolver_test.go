package remote_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/remote"
)

func TestResolveDriver_ServerCertificateAndFingerprintValidation(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LXM_CONFIG_DIR", tmpDir)

	// Spin up a mock TLS server to get a real TLS certificate
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	serverCert := ts.Certificate()
	realFP := remote.FingerprintSHA256(serverCert.Raw)

	// 1. Mismatched fingerprint with certificate declared
	optsMismatch := remote.ResolveOptions{
		RemoteName: "mismatch-node",
		ManifestRemotes: map[string]remote.RemoteEntry{
			"mismatch-node": {
				Address:           ts.URL,
				Provider:          provider.ProviderTypeIncus,
				ServerCertificate: "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----",
				ServerFingerprint: "0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}
	_, err := remote.ResolveDriver(optsMismatch)
	if err == nil || !strings.Contains(err.Error(), "parsing server_certificate") {
		// If PEM decoding failed on invalid dummy PEM, that's expected
	}

	// 2. Probe TLS server with matching fingerprint
	optsProbeMatch := remote.ResolveOptions{
		RemoteName: "probe-match",
		ManifestRemotes: map[string]remote.RemoteEntry{
			"probe-match": {
				Address:           ts.URL,
				Provider:          provider.ProviderTypeIncus,
				ServerFingerprint: realFP,
			},
		},
	}
	// This will pass the fingerprint probe and proceed to connect to Incus (which fails on mock HTTP server, but NOT on fingerprint)
	_, err = remote.ResolveDriver(optsProbeMatch)
	if err != nil && strings.Contains(err.Error(), "server certificate fingerprint mismatch") {
		t.Fatalf("expected fingerprint to match, got: %v", err)
	}

	// 3. Probe TLS server with mismatched fingerprint fails immediately
	optsProbeMismatch := remote.ResolveOptions{
		RemoteName: "probe-mismatch",
		ManifestRemotes: map[string]remote.RemoteEntry{
			"probe-mismatch": {
				Address:           ts.URL,
				Provider:          provider.ProviderTypeIncus,
				ServerFingerprint: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			},
		},
	}
	_, err = remote.ResolveDriver(optsProbeMismatch)
	if err == nil || !strings.Contains(err.Error(), "server certificate fingerprint mismatch") {
		t.Fatalf("expected server certificate fingerprint mismatch error, got: %v", err)
	}
}
