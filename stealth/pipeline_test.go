package stealth

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestPipelineFullRoundtrip(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		PacingMode: PacingVideo,
		BufSize:    64,
		Version:    1,
	}

	// Два net.Pipe: имитация DTLS-канала между клиентом и сервером
	clientDTLS, serverDTLS := net.Pipe()
	// WG-стороны
	clientWG_side, clientWG_pipe := net.Pipe()
	serverWG_pipe, serverWG_side := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clientPipe := NewPipeline(cfg)
	serverPipe := NewPipeline(cfg)

	// ВАЖНО: клиент — RunAsClient (шлёт version info),
	// сервер — Run (peek first packet, stealth/legacy detection)
	go func() { _ = clientPipe.RunAsClient(ctx, clientDTLS, clientWG_pipe) }()
	go func() { _ = serverPipe.Run(ctx, serverDTLS, serverWG_pipe) }()

	// Клиент отправляет данные через WG
	sent := []byte("vpn packet data")
	go func() { _, _ = clientWG_side.Write(sent) }()

	// Сервер получает на WG-стороне — Pipeline убирает version info
	buf := make([]byte, 1600)
	_ = serverWG_side.SetReadDeadline(time.Now().Add(time.Second))
	n, err := serverWG_side.Read(buf)
	if err != nil {
		t.Fatalf("server WG read: %v", err)
	}
	if string(buf[:n]) != string(sent) {
		t.Fatalf("roundtrip: got %q, want %q", buf[:n], sent)
	}

	cancel()
	clientWG_side.Close()
	serverWG_side.Close()
	clientDTLS.Close()
	serverDTLS.Close()
}

func TestPipelineRunAsClient(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		PacingMode: PacingVideo,
		BufSize:    64,
		Version:    1,
	}

	clientDTLS, dtlsSide := net.Pipe()
	clientWG_side, clientWG_pipe := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipe := NewPipeline(cfg)
	go func() { _ = pipe.RunAsClient(ctx, clientDTLS, clientWG_pipe) }()

	// Отправляем данные в WG
	sent := []byte("test data")
	go func() { _, _ = clientWG_side.Write(sent) }()

	// Первый фрейм с DTLS-стороны содержит version info + данные
	f := &Framer{}
	buf := make([]byte, 1600)
	_ = dtlsSide.SetReadDeadline(time.Now().Add(time.Second))
	n, err := dtlsSide.Read(buf)
	if err != nil {
		t.Fatalf("dtls read: %v", err)
	}
	flags, payload, err := f.Decode(buf[:n])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if flags != FlagData {
		t.Fatalf("flags: got 0x%02x", flags)
	}
	// Version info (4 байта) + реальные данные
	vcfg, ok := f.DecodeVersionInfo(payload)
	if !ok {
		t.Fatal("version info not found in first packet")
	}
	if vcfg.Version != 1 || vcfg.PacingMode != PacingVideo {
		t.Fatalf("version: v%d pacing=%s", vcfg.Version, vcfg.PacingMode)
	}
	remaining := payload[versionInfoSize:]
	if string(remaining) != string(sent) {
		t.Fatalf("data: got %q, want %q", remaining, sent)
	}

	cancel()
	clientWG_side.Close()
	dtlsSide.Close()
}

func TestPipelineLegacyFallback(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		PacingMode: PacingVideo,
		BufSize:    64,
		Version:    1,
	}

	serverWG_pipe, serverWG_side := net.Pipe()

	// Имитируем legacy-клиент: первый байт = 0x01 (WireGuard handshake)
	legacyDTLS, serverDTLSend := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipe := NewPipeline(cfg)

	go func() { _ = pipe.Run(ctx, serverDTLSend, serverWG_pipe) }()

	// legacy-клиент отправляет WireGuard handshake (начинается с 0x01)
	wgPacket := []byte{0x01, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03}
	go func() { _, _ = legacyDTLS.Write(wgPacket) }()

	// Сервер должен передать пакет в WG как есть (legacy fallback)
	buf := make([]byte, 1600)
	_ = serverWG_side.SetReadDeadline(time.Now().Add(time.Second))
	n, err := serverWG_side.Read(buf)
	if err != nil {
		t.Fatalf("legacy WG read: %v", err)
	}
	if string(buf[:n]) != string(wgPacket) {
		t.Fatalf("legacy fallback: got %v, want %v", buf[:n], wgPacket)
	}

	cancel()
	serverDTLSend.Close()
	legacyDTLS.Close()
	serverWG_side.Close()
}

func TestPipelineMetrics_NilPacer(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		PacingMode: PacingVideo,
		BufSize:    64,
		Version:    1,
	}
	pipe := NewPipeline(cfg)
	m := pipe.Metrics()
	if m.BufLen != 0 || m.BufCap != 0 {
		t.Fatalf("nil pacer: got {%d, %d}, want {0, 0}", m.BufLen, m.BufCap)
	}
}

func TestPipelineMetrics_DelegatesToPacer(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		PacingMode: PacingVideo,
		BufSize:    16,
		Version:    1,
	}

	clientDTLS, dtlsSide := net.Pipe()
	clientWG_side, clientWG_pipe := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipe := NewPipeline(cfg)
	go func() { _ = pipe.RunAsClient(ctx, clientDTLS, clientWG_pipe) }()

	// Даём время стартовать
	time.Sleep(50 * time.Millisecond)

	m := pipe.Metrics()
	if m.BufCap == 0 {
		t.Fatal("Pipeline.Metrics().BufCap == 0 after RunAsClient started")
	}
	// BufCap должен быть 16 (bufSize из Config)
	if m.BufCap != 16 {
		t.Fatalf("Pipeline.Metrics().BufCap: got %d, want 16", m.BufCap)
	}

	cancel()
	clientWG_side.Close()
	dtlsSide.Close()
}

func TestPipelineDummyDoesNotLeak(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		PacingMode: PacingVideo,
		BufSize:    64,
		Version:    1,
	}

	clientDTLS, serverDTLS := net.Pipe()
	_, clientWG_pipe := net.Pipe()
	serverWG_pipe, serverWG_side := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clientPipe := NewPipeline(cfg)
	serverPipe := NewPipeline(cfg)

	go func() { _ = clientPipe.Run(ctx, clientDTLS, clientWG_pipe) }()
	go func() { _ = serverPipe.Run(ctx, serverDTLS, serverWG_pipe) }()

	// Не отправляем данных — только dummy-пакеты должны генерироваться
	// WG-сторона сервера не должна получать ничего
	_ = serverWG_side.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1600)
	_, err := serverWG_side.Read(buf)
	if err == nil {
		t.Fatal("dummy пакет протёк до WG-стороны")
	}
	// Timeout — ожидаемо, dummy не должен доходить

	cancel()
	clientDTLS.Close()
	serverDTLS.Close()
	serverWG_side.Close()
}
