package stealth

import (
	"context"
	"net"
	"testing"
	"time"
)

// helperPacer создаёт Pacer с video mode для тестов.
func helperPacer(bufSize int) *Pacer {
	f := &Framer{}
	p := NewPadder(DefaultPadderConfig())
	return NewPacer(PacerConfig{
		Mode:    PacingVideo,
		BufSize: bufSize,
	}, f, p)
}

func TestPacerDataDelivery(t *testing.T) {
	pacer := helperPacer(64)
	// wgSide <-> wgPipe | dtlsPipe <-> dtlsSide
	wgSide, wgPipe := net.Pipe()
	dtlsPipe, dtlsSide := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- pacer.Run(ctx, wgPipe, dtlsPipe) }()

	// Отправляем данные в WG-сторону
	sent := []byte("test payload data")
	go func() { _, _ = wgSide.Write(sent) }()

	// Читаем с DTLS-стороны — должен прийти stealth-фрейм
	buf := make([]byte, 1600)
	_ = dtlsSide.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	n, err := dtlsSide.Read(buf)
	if err != nil {
		t.Fatalf("read from dtls side: %v", err)
	}

	// Декодируем фрейм
	f := &Framer{}
	flags, payload, err := f.Decode(buf[:n])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if flags != FlagData {
		t.Fatalf("flags: got 0x%02x, want 0x%02x", flags, FlagData)
	}
	if string(payload) != string(sent) {
		t.Fatalf("payload: got %q, want %q", payload, sent)
	}

	cancel()
	wgSide.Close()
	dtlsSide.Close()
}

func TestPacerDummyGeneration(t *testing.T) {
	pacer := helperPacer(64)
	_, wgPipe := net.Pipe()
	dtlsPipe, dtlsSide := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = pacer.Run(ctx, wgPipe, dtlsPipe) }()

	// Не отправляем данных — ждём dummy-пакеты
	f := &Framer{}
	buf := make([]byte, 1600)
	_ = dtlsSide.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	n, err := dtlsSide.Read(buf)
	if err != nil {
		t.Fatalf("read dummy: %v", err)
	}

	flags, payload, err := f.Decode(buf[:n])
	if err != nil {
		t.Fatalf("decode dummy: %v", err)
	}
	if flags != FlagDummy {
		t.Fatalf("dummy flags: got 0x%02x, want 0x%02x", flags, FlagDummy)
	}
	if len(payload) != 0 {
		t.Fatalf("dummy payload len: got %d, want 0", len(payload))
	}

	cancel()
	wgPipe.Close()
	dtlsSide.Close()
}

func TestPacerInboundDecodeAndDummyFilter(t *testing.T) {
	pacer := helperPacer(64)
	wgSide, wgPipe := net.Pipe()
	dtlsPipe, dtlsSide := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = pacer.Run(ctx, wgPipe, dtlsPipe) }()

	f := &Framer{}
	p := NewPadder(DefaultPadderConfig())

	// Отправляем dummy-фрейм в DTLS-сторону — не должен дойти до WG
	dummyFrame := f.Encode(FlagDummy, nil, p.TargetSize(0))
	_, _ = dtlsSide.Write(dummyFrame)

	// Отправляем data-фрейм — должен дойти
	realData := []byte("real data")
	dataFrame := f.Encode(FlagData, realData, p.TargetSize(len(realData)))
	_, _ = dtlsSide.Write(dataFrame)

	buf := make([]byte, 1600)
	_ = wgSide.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	n, err := wgSide.Read(buf)
	if err != nil {
		t.Fatalf("read from wg side: %v", err)
	}
	if string(buf[:n]) != string(realData) {
		t.Fatalf("got %q, want %q", buf[:n], realData)
	}

	cancel()
	wgSide.Close()
	dtlsSide.Close()
}

func TestPacerGracefulShutdown(t *testing.T) {
	pacer := helperPacer(64)
	_, wgPipe := net.Pipe()
	dtlsPipe, dtlsSide := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- pacer.Run(ctx, wgPipe, dtlsPipe) }()

	// Даём время стартовать
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			t.Fatalf("Run вернул неожиданную ошибку: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run не завершился после cancel")
	}

	wgPipe.Close()
	dtlsSide.Close()
}

func TestPacerMetrics_BeforeRun(t *testing.T) {
	pacer := helperPacer(64)
	m := pacer.Metrics()
	if m.BufLen != 0 || m.BufCap != 0 {
		t.Fatalf("before run: got {%d, %d}, want {0, 0}", m.BufLen, m.BufCap)
	}
}

func TestPacerMetrics_Running(t *testing.T) {
	pacer := helperPacer(8) // маленький буфер для быстрого заполнения
	_, wgPipe := net.Pipe()
	dtlsPipe, dtlsSide := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = pacer.Run(ctx, wgPipe, dtlsPipe) }()

	// Даём Pacer время стартовать и создать outCh
	time.Sleep(50 * time.Millisecond)

	// BufCap должен быть > 0 после запуска
	m := pacer.Metrics()
	if m.BufCap == 0 {
		t.Fatal("BufCap == 0 after run started")
	}

	cancel()
	wgPipe.Close()
	dtlsSide.Close()
}

func TestPacerMetrics_MixedMode(t *testing.T) {
	f := &Framer{}
	p := NewPadder(DefaultPadderConfig())
	pacer := NewPacer(PacerConfig{
		Mode:    PacingMixed,
		BufSize: 16,
	}, f, p)

	_, wgPipe := net.Pipe()
	dtlsPipe, dtlsSide := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = pacer.Run(ctx, wgPipe, dtlsPipe) }()

	time.Sleep(50 * time.Millisecond)

	m := pacer.Metrics()
	// Mixed mode: BufCap = cap(audioCh) + cap(videoCh) = 16 + 16 = 32
	if m.BufCap != 32 {
		t.Fatalf("mixed BufCap: got %d, want 32", m.BufCap)
	}

	cancel()
	wgPipe.Close()
	dtlsSide.Close()
}

func TestPacerMixedMode(t *testing.T) {
	f := &Framer{}
	p := NewPadder(DefaultPadderConfig())
	pacer := NewPacer(PacerConfig{
		Mode:    PacingMixed,
		BufSize: 64,
	}, f, p)

	wgSide, wgPipe := net.Pipe()
	dtlsPipe, dtlsSide := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = pacer.Run(ctx, wgPipe, dtlsPipe) }()

	// Отправляем маленький пакет (аудио) и большой (видео)
	go func() {
		_, _ = wgSide.Write([]byte("small")) // <= 200 — аудио
		time.Sleep(10 * time.Millisecond)
		_, _ = wgSide.Write(make([]byte, 500)) // > 200 — видео
	}()

	// Читаем два фрейма с DTLS-стороны
	framer := &Framer{}
	buf := make([]byte, 1600)
	received := 0
	deadline := time.Now().Add(500 * time.Millisecond)
	for received < 2 && time.Now().Before(deadline) {
		_ = dtlsSide.SetReadDeadline(deadline)
		n, err := dtlsSide.Read(buf)
		if err != nil {
			break
		}
		flags, _, err := framer.Decode(buf[:n])
		if err != nil {
			continue
		}
		if flags == FlagData {
			received++
		}
	}
	if received < 2 {
		t.Fatalf("получено %d data-пакетов, ожидалось 2", received)
	}

	cancel()
	wgSide.Close()
	dtlsSide.Close()
}
