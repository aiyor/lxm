package remote_test

import (
	"bytes"
	"encoding/pem"
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

	var certPEMBuf bytes.Buffer
	if err := pem.Encode(&certPEMBuf, &pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Raw}); err != nil {
		t.Fatalf("encoding server cert to PEM: %v", err)
	}
	validPEM := certPEMBuf.String()

	// 1. Mismatched fingerprint with valid certificate declared
	optsMismatch := remote.ResolveOptions{
		RemoteName: "mismatch-node",
		ManifestRemotes: map[string]remote.RemoteEntry{
			"mismatch-node": {
				Address:           ts.URL,
				Provider:          provider.ProviderTypeIncus,
				ServerCertificate: validPEM,
				ServerFingerprint: "0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}
	_, err := remote.ResolveDriver(optsMismatch)
	if err == nil || !strings.Contains(err.Error(), "server_fingerprint mismatch") {
		t.Fatalf("expected server_fingerprint mismatch error on declared cert+fp, got: %v", err)
	}

	// 2. Probe TLS server with matching fingerprint (pins certificate into SDK connection)
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
	_, err = remote.ResolveDriver(optsProbeMatch)
	if err != nil {
		if strings.Contains(err.Error(), "unknown authority") {
			t.Fatalf("connection failed with unknown authority despite pinned certificate: %v", err)
		}
		if strings.Contains(err.Error(), "fingerprint mismatch") {
			t.Fatalf("expected fingerprint to match, got: %v", err)
		}
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

	// 4. UNIX socket with server_fingerprint is rejected
	optsUnixFP := remote.ResolveOptions{
		RemoteName: "unix-fp",
		ManifestRemotes: map[string]remote.RemoteEntry{
			"unix-fp": {
				Address:           "unix:///var/lib/incus/unix.socket",
				Provider:          provider.ProviderTypeIncus,
				ServerFingerprint: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			},
		},
	}
	_, err = remote.ResolveDriver(optsUnixFP)
	if err == nil || !strings.Contains(err.Error(), "UNIX socket endpoint") {
		t.Fatalf("expected error rejecting server_fingerprint on UNIX socket, got: %v", err)
	}

	// 5. Insecure true combined with server_fingerprint is rejected as contradictory
	optsInsecureFP := remote.ResolveOptions{
		RemoteName: "insecure-fp",
		ManifestRemotes: map[string]remote.RemoteEntry{
			"insecure-fp": {
				Address:           ts.URL,
				Provider:          provider.ProviderTypeIncus,
				Insecure:          true,
				ServerFingerprint: realFP,
			},
		},
	}
	_, err = remote.ResolveDriver(optsInsecureFP)
	if err == nil || !strings.Contains(err.Error(), "contradictory configuration") {
		t.Fatalf("expected error rejecting insecure: true with server_fingerprint, got: %v", err)
	}
}
