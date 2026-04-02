package stealth

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"log"
	"net"
	"sync"
	"time"
)

// Интервалы pacing.
const (
	audioInterval = 20 * time.Millisecond
	videoInterval = 33 * time.Millisecond
	jitterRange   = 2 * time.Millisecond // ±2ms
)

// PacerConfig — конфигурация Pacer.
type PacerConfig struct {
	Mode    PacingMode
	BufSize int // ёмкость буфера, default 64
}

// Pacer управляет ритмичной отправкой пакетов с буферизацией и dummy-генерацией.
type Pacer struct {
	mode    PacingMode
	framer  *Framer
	padder  *Padder
	bufSize int
	// Каналы буфера — промоутены из локальных переменных для экспорта метрик.
	// Инициализируются в run()/runMixed(). До этого — nil.
	outCh   chan []byte // non-mixed mode
	audioCh chan []byte // mixed mode
	videoCh chan []byte // mixed mode
}

// NewPacer создаёт Pacer.
func NewPacer(cfg PacerConfig, framer *Framer, padder *Padder) *Pacer {
	bufSize := cfg.BufSize
	if bufSize <= 0 {
		bufSize = 64
	}
	return &Pacer{
		mode:    cfg.Mode,
		framer:  framer,
		padder:  padder,
		bufSize: bufSize,
	}
}

// Metrics возвращает приблизительный snapshot утилизации буфера.
// До запуска run() возвращает {0, 0}. Потокобезопасен без мьютекса.
func (p *Pacer) Metrics() PacerMetrics {
	if p.mode == PacingMixed {
		if p.audioCh == nil || p.videoCh == nil {
			return PacerMetrics{}
		}
		return PacerMetrics{
			BufLen: len(p.audioCh) + len(p.videoCh),
			BufCap: cap(p.audioCh) + cap(p.videoCh),
		}
	}
	if p.outCh == nil {
		return PacerMetrics{}
	}
	return PacerMetrics{
		BufLen: len(p.outCh),
		BufCap: cap(p.outCh),
	}
}

// intervalForMode возвращает pacing-интервал для режима.
func intervalForMode(mode PacingMode) time.Duration {
	switch mode {
	case PacingAudio:
		return audioInterval
	case PacingVideo:
		return videoInterval
	default:
		return videoInterval
	}
}

// jitteredInterval добавляет случайный jitter ±2ms к интервалу.
func jitteredInterval(base time.Duration) time.Duration {
	var buf [2]byte
	_, _ = rand.Read(buf[:])
	jitter := time.Duration(int(binary.BigEndian.Uint16(buf[:]))%int(jitterRange*2+1)) - jitterRange
	result := base + jitter
	if result < time.Millisecond {
		result = time.Millisecond
	}
	return result
}

// Run запускает bidirectional relay с pacing на исходящем направлении.
// outbound: src (WG) -> буфер -> pacing timer -> encode -> dst (DTLS)
// inbound: dst (DTLS) -> decode -> отбросить dummy -> src (WG)
// Блокирует до отмены ctx или ошибки сети.
func (p *Pacer) Run(ctx context.Context, src net.Conn, dst net.Conn) error {
	return p.run(ctx, src, dst, nil)
}

// run — внутренняя реализация Run. firstPacket — уже прочитанный первый пакет (для сервера).
func (p *Pacer) run(ctx context.Context, src net.Conn, dst net.Conn, firstInboundPacket []byte) error {
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 4)

	// Закрываем соединения при отмене контекста, чтобы разблокировать Read/Write
	context.AfterFunc(ctx2, func() {
		_ = src.SetDeadline(time.Now())
		_ = dst.SetDeadline(time.Now())
	})

	if p.mode == PacingMixed {
		return p.runMixed(ctx2, cancel, src, dst, firstInboundPacket, &wg, errCh)
	}

	interval := intervalForMode(p.mode)
	p.outCh = make(chan []byte, p.bufSize)
	// Единый output channel для записи в dst
	writeCh := make(chan []byte, p.bufSize)

	// outbound: src -> outCh (readLoop)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 1600)
		for {
			select {
			case <-ctx2.Done():
				return
			default:
			}
			n, err := src.Read(buf)
			if err != nil {
				select {
				case <-ctx2.Done():
				default:
					errCh <- err
				}
				return
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			select {
			case p.outCh <- pkt:
			case <-ctx2.Done():
				return
			}
		}
	}()

	// pacingLoop: p.outCh -> encode -> writeCh
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			wait := jitteredInterval(interval)
			timer := time.NewTimer(wait)
			select {
			case <-ctx2.Done():
				timer.Stop()
				return
			case <-timer.C:
			}

			select {
			case data := <-p.outCh:
				// Data пакет
				targetSize := p.padder.TargetSize(len(data))
				frame := p.framer.Encode(FlagData, data, targetSize)
				select {
				case writeCh <- frame:
				case <-ctx2.Done():
					return
				}
			default:
				// Нет данных — dummy (payload=nil, original_payload_len=0, шум в padding)
				targetSize := p.padder.TargetSize(0) // AudioProfile для dummy
				frame := p.framer.Encode(FlagDummy, nil, targetSize)
				select {
				case writeCh <- frame:
				case <-ctx2.Done():
					return
				}
			}
		}
	}()

	// writerLoop: writeCh -> dst.Write (единственный writer)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			select {
			case frame := <-writeCh:
				_, err := dst.Write(frame)
				if err != nil {
					select {
					case <-ctx2.Done():
					default:
						errCh <- err
					}
					return
				}
			case <-ctx2.Done():
				// Drain буфер
				drainDeadline := time.After(time.Duration(float64(p.bufSize) * float64(interval) * 1.5))
				for {
					select {
					case frame := <-writeCh:
						_, _ = dst.Write(frame)
					case <-drainDeadline:
						return
					default:
						return
					}
				}
			}
		}
	}()

	// inbound: dst -> decode -> фильтр dummy -> src
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		// Обработать первый пакет, если есть
		if firstInboundPacket != nil {
			if err := p.processInbound(firstInboundPacket, src); err != nil {
				errCh <- err
				return
			}
		}
		buf := make([]byte, 1600)
		for {
			select {
			case <-ctx2.Done():
				return
			default:
			}
			n, err := dst.Read(buf)
			if err != nil {
				select {
				case <-ctx2.Done():
				default:
					errCh <- err
				}
				return
			}
			if err := p.processInbound(buf[:n], src); err != nil {
				log.Printf("stealth: невалидный фрейм (%d байт), отброшен", n)
			}
		}
	}()

	wg.Wait()

	select {
	case err := <-errCh:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	default:
		return ctx.Err()
	}
}

// processInbound декодирует stealth-фрейм и отправляет payload в src (WG).
// Dummy и keepalive пакеты отбрасываются.
func (p *Pacer) processInbound(frame []byte, src net.Conn) error {
	flags, payload, err := p.framer.Decode(frame)
	if err != nil {
		return err
	}
	// Dummy и keepalive — отбрасываем
	if flags != FlagData {
		return nil
	}
	if len(payload) == 0 {
		return nil
	}
	_, err = src.Write(payload)
	return err
}

// runMixed — pacing с двумя таймерами (audio + video) и единым output channel.
func (p *Pacer) runMixed(ctx context.Context, cancel context.CancelFunc, src net.Conn, dst net.Conn, firstInboundPacket []byte, wg *sync.WaitGroup, errCh chan error) error {
	p.audioCh = make(chan []byte, p.bufSize)
	p.videoCh = make(chan []byte, p.bufSize)
	writeCh := make(chan []byte, p.bufSize*2)

	// readLoop: src -> audio/videoCh по размеру пакета
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 1600)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := src.Read(buf)
			if err != nil {
				select {
				case <-ctx.Done():
				default:
					errCh <- err
				}
				return
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			if n <= 200 {
				select {
				case p.audioCh <- pkt:
				case <-ctx.Done():
					return
				}
			} else {
				select {
				case p.videoCh <- pkt:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// audioPacingLoop
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		p.pacingLoop(ctx, p.audioCh, writeCh, audioInterval)
	}()

	// videoPacingLoop
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		p.pacingLoop(ctx, p.videoCh, writeCh, videoInterval)
	}()

	// writerLoop (с drain при shutdown)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			select {
			case frame := <-writeCh:
				_, err := dst.Write(frame)
				if err != nil {
					select {
					case <-ctx.Done():
					default:
						errCh <- err
					}
					return
				}
			case <-ctx.Done():
				// Drain буфер
				drainDeadline := time.After(time.Duration(float64(p.bufSize) * float64(videoInterval) * 1.5))
				for {
					select {
					case frame := <-writeCh:
						_, _ = dst.Write(frame)
					case <-drainDeadline:
						return
					default:
						return
					}
				}
			}
		}
	}()

	// inbound
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		if firstInboundPacket != nil {
			if err := p.processInbound(firstInboundPacket, src); err != nil {
				log.Printf("stealth: невалидный фрейм (%d байт), отброшен", len(firstInboundPacket))
			}
		}
		buf := make([]byte, 1600)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := dst.Read(buf)
			if err != nil {
				select {
				case <-ctx.Done():
				default:
					errCh <- err
				}
				return
			}
			if err := p.processInbound(buf[:n], src); err != nil {
				log.Printf("stealth: невалидный фрейм (%d байт), отброшен", n)
			}
		}
	}()

	wg.Wait()

	select {
	case err := <-errCh:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	default:
		return ctx.Err()
	}
}

// pacingLoop — общий цикл pacing для одного таймера.
func (p *Pacer) pacingLoop(ctx context.Context, dataCh <-chan []byte, writeCh chan<- []byte, interval time.Duration) {
	for {
		wait := jitteredInterval(interval)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		select {
		case data := <-dataCh:
			targetSize := p.padder.TargetSize(len(data))
			frame := p.framer.Encode(FlagData, data, targetSize)
			select {
			case writeCh <- frame:
			case <-ctx.Done():
				return
			}
		default:
			// Нет данных — dummy
			targetSize := p.padder.TargetSize(0)
			frame := p.framer.Encode(FlagDummy, nil, targetSize)
			select {
			case writeCh <- frame:
			case <-ctx.Done():
				return
			}
		}
	}
}
