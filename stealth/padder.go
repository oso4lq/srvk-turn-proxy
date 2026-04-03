// stealth/padder.go

package stealth

import (
	"crypto/rand"
	"math/big"
)

// SizeProfile — диапазон целевых размеров пакета.
type SizeProfile struct {
	Min int
	Max int
}

// Профили размеров, имитирующие WebRTC-трафик.
var (
	AudioProfile = SizeProfile{Min: 100, Max: 200}  // Opus фреймы
	VideoProfile = SizeProfile{Min: 800, Max: 1400} // VP8/VP9/H.264
)

// audioBoundary — максимальный payload, попадающий в AudioProfile.
// 197 = AudioProfile.Max - headerSize, чтобы payload+header влезал.
const audioBoundary = 197

// PadderConfig — конфигурация Padder.
type PadderConfig struct {
	Audio  SizeProfile
	Video  SizeProfile
	Jitter int // ±jitter байт шума поверх target
}

// DefaultPadderConfig возвращает конфигурацию по умолчанию.
func DefaultPadderConfig() PadderConfig {
	return PadderConfig{
		Audio:  AudioProfile,
		Video:  VideoProfile,
		Jitter: 5,
	}
}

// Padder выбирает целевой размер пакета для имитации WebRTC-трафика.
// Stateless, потокобезопасный.
type Padder struct {
	audio  SizeProfile
	video  SizeProfile
	jitter int
}

// NewPadder создаёт Padder с заданной конфигурацией.
func NewPadder(cfg PadderConfig) *Padder {
	return &Padder{
		audio:  cfg.Audio,
		video:  cfg.Video,
		jitter: cfg.Jitter,
	}
}

// Profile возвращает профиль размера для данного payload.
func (p *Padder) Profile(payloadLen int) SizeProfile {
	if payloadLen <= audioBoundary {
		return p.audio
	}
	return p.video
}

// TargetSize выбирает целевой размер фрейма для payload заданной длины.
func (p *Padder) TargetSize(payloadLen int) int {
	minFrame := payloadLen + headerSize

	// Oversize — не влезает ни в один профиль
	if minFrame > p.video.Max {
		return minFrame
	}

	profile := p.Profile(payloadLen)

	// Если payload+header > profile.Max — переходим в следующий профиль
	if minFrame > profile.Max && profile == p.audio {
		profile = p.video
	}

	lo := profile.Min
	if minFrame > lo {
		lo = minFrame
	}
	hi := profile.Max

	if lo > hi {
		return minFrame
	}

	// Случайный target в [lo, hi]
	target := lo
	if hi > lo {
		target = lo + cryptoRandIntn(hi-lo+1)
	}

	// Jitter ±p.jitter
	jitter := cryptoRandIntn(p.jitter*2+1) - p.jitter
	target += jitter

	// Не меньше lo (profile.Min или minFrame)
	if target < lo {
		target = lo
	}

	return target
}

// cryptoRandIntn возвращает криптографически случайное число в [0, n).
// Использует crypto/rand.Int — без modulo bias при любых n.
func cryptoRandIntn(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}
