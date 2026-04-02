package stealth

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

// Флаги stealth-фреймов. Значения 0xA1-0xA3 выбраны, чтобы не пересекаться
// с WireGuard message types (0x01-0x04).
const (
	FlagData      byte = 0xA1
	FlagDummy     byte = 0xA2
	FlagKeepalive byte = 0xA3
)

// headerSize — размер заголовка stealth-фрейма: flags (1) + payload_len (2).
const headerSize = 3

// maxPayloadLen — максимальный размер payload (ограничен 2 байтами uint16).
const maxPayloadLen = 65535

var (
	errFrameTooShort  = errors.New("stealth: фрейм меньше 3 байт")
	errPayloadTooLong = errors.New("stealth: payload_len превышает размер фрейма")
	errUnknownFlags   = errors.New("stealth: неизвестный флаг")
)

// IsStealthPacket проверяет, является ли первый байт stealth-флагом (0xA1-0xA3).
func IsStealthPacket(firstByte byte) bool {
	return firstByte >= FlagData && firstByte <= FlagKeepalive
}

// PacingMode — режим pacing.
type PacingMode string

const (
	PacingAudio PacingMode = "audio"
	PacingVideo PacingMode = "video"
	PacingMixed PacingMode = "mixed"
)

// versionInfoSize — размер version info в байтах.
const versionInfoSize = 4

// pacingModeToByte конвертирует PacingMode в байт для wire-формата.
func pacingModeToByte(m PacingMode) byte {
	switch m {
	case PacingAudio:
		return 0x01
	case PacingVideo:
		return 0x02
	case PacingMixed:
		return 0x03
	default:
		return 0x02 // default video
	}
}

// byteToPacingMode конвертирует байт из wire-формата в PacingMode.
func byteToPacingMode(b byte) PacingMode {
	switch b {
	case 0x01:
		return PacingAudio
	case 0x02:
		return PacingVideo
	case 0x03:
		return PacingMixed
	default:
		return PacingVideo
	}
}

// VersionConfig — параметры stealth-протокола, передаваемые в первом пакете.
type VersionConfig struct {
	Version    uint8
	PacingMode PacingMode
}

// Framer кодирует и декодирует stealth wire-формат.
// Чистые функции, без горутин, без состояния.
type Framer struct{}

// Encode собирает stealth-фрейм: [flags][payload_len BE][payload][random padding].
// Если targetSize < headerSize+len(payload), фрейм = header+payload (без padding).
func (f *Framer) Encode(flags byte, payload []byte, targetSize int) []byte {
	payloadLen := len(payload)
	frameSize := headerSize + payloadLen
	if targetSize > frameSize {
		frameSize = targetSize
	}

	frame := make([]byte, frameSize)
	frame[0] = flags
	binary.BigEndian.PutUint16(frame[1:3], uint16(payloadLen))

	if payloadLen > 0 {
		copy(frame[headerSize:], payload)
	}

	// Случайный padding
	padStart := headerSize + payloadLen
	if padStart < frameSize {
		_, _ = rand.Read(frame[padStart:])
	}

	return frame
}

// Decode извлекает flags и payload из stealth-фрейма, отбрасывая padding.
func (f *Framer) Decode(frame []byte) (flags byte, payload []byte, err error) {
	if len(frame) < headerSize {
		return 0, nil, errFrameTooShort
	}

	flags = frame[0]
	if !IsStealthPacket(flags) {
		return 0, nil, fmt.Errorf("%w: 0x%02x", errUnknownFlags, flags)
	}

	payloadLen := int(binary.BigEndian.Uint16(frame[1:3]))
	if payloadLen > len(frame)-headerSize {
		return 0, nil, fmt.Errorf("%w: payload_len=%d, available=%d", errPayloadTooLong, payloadLen, len(frame)-headerSize)
	}

	if payloadLen == 0 {
		return flags, nil, nil
	}
	// Копируем payload — исходный буфер может быть перезаписан следующим Read.
	payload = make([]byte, payloadLen)
	copy(payload, frame[headerSize:headerSize+payloadLen])
	return flags, payload, nil
}

// EncodeVersionInfo кодирует version info в 4 байта:
// [version][pacing_mode_byte][reserved][reserved]
func (f *Framer) EncodeVersionInfo(cfg VersionConfig) []byte {
	buf := make([]byte, versionInfoSize)
	buf[0] = cfg.Version
	buf[1] = pacingModeToByte(cfg.PacingMode)
	buf[2] = 0x00 // reserved
	buf[3] = 0x00 // reserved
	return buf
}

// DecodeVersionInfo пытается извлечь version info из начала payload.
// Возвращает false, если payload слишком короткий.
func (f *Framer) DecodeVersionInfo(payload []byte) (VersionConfig, bool) {
	if len(payload) < versionInfoSize {
		return VersionConfig{}, false
	}
	return VersionConfig{
		Version:    payload[0],
		PacingMode: byteToPacingMode(payload[1]),
	}, true
}
