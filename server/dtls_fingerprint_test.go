package main

import (
	"testing"

	"github.com/pion/dtls/v3"
)

// TestServerDTLSFingerprint фиксирует эталонную DTLS-конфигурацию сервера.
// Падает при drift (merge с upstream, рефакторинг).
func TestServerDTLSFingerprint(t *testing.T) {
	cfg := serverDTLSConfig()

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

	// ConnectionIDGenerator — не nil, проверяем что задан
	if cfg.ConnectionIDGenerator == nil {
		t.Fatal("ConnectionIDGenerator: got nil, want RandomCIDGenerator(8)")
	}

	// InsecureSkipVerify — сервер не должен пропускать верификацию
	if cfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify: got true, want false")
	}

	// Certificates — должен быть один сертификат
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates: got %d, want 1", len(cfg.Certificates))
	}
}
