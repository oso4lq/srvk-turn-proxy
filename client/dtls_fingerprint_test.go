// client/dtls_fingerprint_test.go

package main

import (
	"testing"

	"github.com/pion/dtls/v3"
)

// TestClientDTLSFingerprint фиксирует эталонную DTLS-конфигурацию клиента.
func TestClientDTLSFingerprint(t *testing.T) {
	cfg, err := clientDTLSConfig()
	if err != nil {
		t.Fatalf("clientDTLSConfig: %v", err)
	}

	// CipherSuites
	if len(cfg.CipherSuites) != 1 {
		t.Fatalf("CipherSuites: got %d, want 1", len(cfg.CipherSuites))
	}
	if cfg.CipherSuites[0] != dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 {
		t.Fatalf("CipherSuites[0]: got %v, want TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", cfg.CipherSuites[0])
	}

	// ExtendedMasterSecret
	if cfg.ExtendedMasterSecret != dtls.RequireExtendedMasterSecret {
		t.Fatalf("ExtendedMasterSecret: got %v, want RequireExtendedMasterSecret", cfg.ExtendedMasterSecret)
	}

	// ConnectionIDGenerator — не nil
	if cfg.ConnectionIDGenerator == nil {
		t.Fatal("ConnectionIDGenerator: got nil, want OnlySendCIDGenerator()")
	}

	// InsecureSkipVerify — клиент: true (self-signed серверный сертификат)
	if !cfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify: got false, want true")
	}

	// Certificates — должен быть один сертификат
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates: got %d, want 1", len(cfg.Certificates))
	}
}
