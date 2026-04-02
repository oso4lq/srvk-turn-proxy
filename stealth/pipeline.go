package stealth

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config — конфигурация stealth-слоя.
type Config struct {
	Enabled    bool
	PacingMode PacingMode
	BufSize    int
	Version    uint8
}

// DefaultConfig возвращает конфигурацию по умолчанию.
func DefaultConfig() Config {
	return Config{
		Enabled:    false,
		PacingMode: PacingVideo,
		BufSize:    64,
		Version:    1,
	}
}

// ResolveConfig читает конфигурацию: CLI-флаги приоритетнее, fallback на env vars.
// Вызывается из main() после flag.Parse().
// CLI-значения передаются как аргументы; если не заданы — читаем env.
func ResolveConfig(stealthFlag *bool, pacingFlag *string, bufFlag *int) Config {
	cfg := DefaultConfig()

	// CLI-флаги приоритетнее
	if stealthFlag != nil && *stealthFlag {
		cfg.Enabled = true
	} else if env := os.Getenv("STEALTH_MODE"); strings.EqualFold(env, "true") {
		cfg.Enabled = true
	}

	if pacingFlag != nil && *pacingFlag != "" {
		cfg.PacingMode = parsePacingMode(*pacingFlag)
	} else if env := os.Getenv("STEALTH_PACING_MODE"); env != "" {
		cfg.PacingMode = parsePacingMode(env)
	}

	if bufFlag != nil && *bufFlag > 0 {
		cfg.BufSize = *bufFlag
	} else if env := os.Getenv("STEALTH_BUF_SIZE"); env != "" {
		if v, err := strconv.Atoi(env); err == nil && v > 0 {
			cfg.BufSize = v
		}
	}

	return cfg
}

// parsePacingMode валидирует строку pacing mode. При невалидном значении
// логирует предупреждение и возвращает PacingVideo.
func parsePacingMode(s string) PacingMode {
	switch PacingMode(s) {
	case PacingAudio, PacingVideo, PacingMixed:
		return PacingMode(s)
	default:
		log.Printf("stealth: неизвестный pacing mode %q, используется %q", s, PacingVideo)
		return PacingVideo
	}
}

// Pipeline компонует Framer, Padder и Pacer в единый relay.
type Pipeline struct {
	framer *Framer
	padder *Padder
	cfg    Config
	pacer  *Pacer // инициализируется в runStealth()/RunAsClient()
}

// NewPipeline создаёт Pipeline с заданной конфигурацией.
func NewPipeline(cfg Config) *Pipeline {
	return &Pipeline{
		framer: &Framer{},
		padder: NewPadder(DefaultPadderConfig()),
		cfg:    cfg,
	}
}

// Metrics возвращает метрики Pacer. До запуска Pipeline возвращает {0, 0}.
func (p *Pipeline) Metrics() PacerMetrics {
	if p.pacer == nil {
		return PacerMetrics{}
	}
	return p.pacer.Metrics()
}

// Run запускает stealth relay между dtlsConn и wgConn.
// Первый входящий пакет проверяется на stealth/legacy.
// Блокирует до отмены ctx или ошибки.
func (p *Pipeline) Run(ctx context.Context, dtlsConn net.Conn, wgConn net.Conn) error {
	log.Printf("stealth: mode=%t pacing=%s buf=%d version=%d",
		p.cfg.Enabled, p.cfg.PacingMode, p.cfg.BufSize, p.cfg.Version)

	// Читаем первый пакет для определения stealth/legacy
	buf := make([]byte, 1600)
	n, err := dtlsConn.Read(buf)
	if err != nil {
		return fmt.Errorf("stealth: чтение первого пакета: %w", err)
	}
	firstPacket := make([]byte, n)
	copy(firstPacket, buf[:n])

	if !IsStealthPacket(firstPacket[0]) {
		// Legacy-клиент: прямой relay
		log.Printf("stealth: legacy клиент, прямой relay")
		// Отправляем первый пакет в WG
		if _, err := wgConn.Write(firstPacket); err != nil {
			return err
		}
		return p.legacyRelay(ctx, dtlsConn, wgConn)
	}

	// Stealth-клиент: запускаем Pipeline с pacing
	return p.runStealth(ctx, dtlsConn, wgConn, firstPacket)
}

// RunWithFirstPacket — как Run, но первый пакет из dtlsConn уже прочитан.
func (p *Pipeline) RunWithFirstPacket(ctx context.Context, dtlsConn net.Conn, wgConn net.Conn, firstPacket []byte) error {
	log.Printf("stealth: mode=%t pacing=%s buf=%d version=%d",
		p.cfg.Enabled, p.cfg.PacingMode, p.cfg.BufSize, p.cfg.Version)

	if !IsStealthPacket(firstPacket[0]) {
		log.Printf("stealth: legacy клиент, прямой relay")
		if _, err := wgConn.Write(firstPacket); err != nil {
			return err
		}
		return p.legacyRelay(ctx, dtlsConn, wgConn)
	}

	return p.runStealth(ctx, dtlsConn, wgConn, firstPacket)
}

// runStealth запускает stealth relay. Обрабатывает version info из первого пакета,
// затем запускает Pacer.
func (p *Pipeline) runStealth(ctx context.Context, dtlsConn net.Conn, wgConn net.Conn, firstPacket []byte) error {
	// Декодируем первый пакет
	flags, payload, err := p.framer.Decode(firstPacket)
	if err != nil {
		return fmt.Errorf("stealth: декодирование первого пакета: %w", err)
	}

	// Извлекаем version info
	if flags == FlagData && len(payload) >= versionInfoSize {
		vcfg, ok := p.framer.DecodeVersionInfo(payload)
		if ok {
			log.Printf("stealth: клиент v%d, pacing=%s", vcfg.Version, vcfg.PacingMode)
			// Данные после version info
			remaining := payload[versionInfoSize:]
			if len(remaining) > 0 {
				if _, err := wgConn.Write(remaining); err != nil {
					return err
				}
			}
		} else {
			// Невалидный version info — передаём payload целиком
			if len(payload) > 0 {
				if _, err := wgConn.Write(payload); err != nil {
					return err
				}
			}
		}
	} else if flags == FlagData && len(payload) > 0 {
		// Data без version info
		if _, err := wgConn.Write(payload); err != nil {
			return err
		}
	}
	// Dummy/keepalive первый пакет — отбрасываем

	p.pacer = NewPacer(PacerConfig{
		Mode:    p.cfg.PacingMode,
		BufSize: p.cfg.BufSize,
	}, p.framer, p.padder)

	return p.pacer.run(ctx, wgConn, dtlsConn, nil)
}

// RunAsClient запускает stealth relay на стороне клиента.
// Первый исходящий пакет обогащается version info.
func (p *Pipeline) RunAsClient(ctx context.Context, dtlsConn net.Conn, wgConn net.Conn) error {
	log.Printf("stealth: mode=%t pacing=%s buf=%d version=%d",
		p.cfg.Enabled, p.cfg.PacingMode, p.cfg.BufSize, p.cfg.Version)

	p.pacer = NewPacer(PacerConfig{
		Mode:    p.cfg.PacingMode,
		BufSize: p.cfg.BufSize,
	}, p.framer, p.padder)

	// Клиентская сторона: оборачиваем wgConn для prepend version info в первый пакет
	wrappedWG := &versionPrependConn{
		Conn:   wgConn,
		framer: p.framer,
		cfg: VersionConfig{
			Version:    p.cfg.Version,
			PacingMode: p.cfg.PacingMode,
		},
		first: true,
	}

	return p.pacer.run(ctx, wrappedWG, dtlsConn, nil)
}

// versionPrependConn оборачивает net.Conn и добавляет version info к первому Read.
type versionPrependConn struct {
	net.Conn
	framer *Framer
	cfg    VersionConfig
	first  bool
}

func (v *versionPrependConn) Read(b []byte) (int, error) {
	n, err := v.Conn.Read(b)
	if err != nil {
		return n, err
	}
	if v.first {
		v.first = false
		vinfo := v.framer.EncodeVersionInfo(v.cfg)
		total := len(vinfo) + n
		if total > len(b) {
			return n, fmt.Errorf("stealth: буфер слишком мал для version info prepend (%d < %d)", len(b), total)
		}
		// Сдвигаем данные вправо, вставляем vinfo в начало
		copy(b[len(vinfo):total], b[:n])
		copy(b[:len(vinfo)], vinfo)
		return total, nil
	}
	return n, err
}

// legacyRelay — прямой bidirectional relay без stealth.
func (p *Pipeline) legacyRelay(ctx context.Context, dtlsConn net.Conn, wgConn net.Conn) error {
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()

	context.AfterFunc(ctx2, func() {
		_ = dtlsConn.SetDeadline(time.Now())
		_ = wgConn.SetDeadline(time.Now())
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 1600)
		for {
			_ = dtlsConn.SetReadDeadline(time.Now().Add(30 * time.Minute))
			n, err := dtlsConn.Read(buf)
			if err != nil {
				return
			}
			_ = wgConn.SetWriteDeadline(time.Now().Add(30 * time.Minute))
			if _, err := wgConn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 1600)
		for {
			_ = wgConn.SetReadDeadline(time.Now().Add(30 * time.Minute))
			n, err := wgConn.Read(buf)
			if err != nil {
				return
			}
			_ = dtlsConn.SetWriteDeadline(time.Now().Add(30 * time.Minute))
			if _, err := dtlsConn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	wg.Wait()
	return ctx.Err()
}
