package stealth

import (
	"testing"
)

func TestFramerEncodeDecodeRoundtrip(t *testing.T) {
	f := &Framer{}
	payload := []byte("hello wireguard")
	frame := f.Encode(FlagData, payload, 200)

	flags, decoded, err := f.Decode(frame)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	if flags != FlagData {
		t.Fatalf("flags: got 0x%02x, want 0x%02x", flags, FlagData)
	}
	if string(decoded) != string(payload) {
		t.Fatalf("payload: got %q, want %q", decoded, payload)
	}
}

func TestFramerEncodeTargetSize(t *testing.T) {
	f := &Framer{}
	payload := []byte("test")
	frame := f.Encode(FlagData, payload, 100)
	if len(frame) != 100 {
		t.Fatalf("frame size: got %d, want 100", len(frame))
	}
}

func TestFramerEncodePayloadLargerThanTarget(t *testing.T) {
	f := &Framer{}
	payload := make([]byte, 200)
	// targetSize меньше payload+header — фрейм = payload + header
	frame := f.Encode(FlagData, payload, 100)
	if len(frame) != 200+headerSize {
		t.Fatalf("frame size: got %d, want %d", len(frame), 200+headerSize)
	}
}

func TestFramerEncodePaddingIsRandom(t *testing.T) {
	f := &Framer{}
	payload := []byte("same")
	frame1 := f.Encode(FlagData, payload, 200)
	frame2 := f.Encode(FlagData, payload, 200)
	// Padding часть (после header+payload) должна отличаться
	padStart := headerSize + len(payload)
	same := true
	for i := padStart; i < len(frame1); i++ {
		if frame1[i] != frame2[i] {
			same = false
			break
		}
	}
	if same && len(frame1) > padStart+1 {
		t.Fatal("padding идентичен в двух вызовах — не случайный")
	}
}

func TestFramerDecodeErrors(t *testing.T) {
	f := &Framer{}

	// Фрейм < 3 байт
	_, _, err := f.Decode([]byte{0xA1, 0x00})
	if err == nil {
		t.Fatal("ожидалась ошибка для фрейма < 3 байт")
	}

	// payload_len > размер фрейма
	bad := []byte{0xA1, 0x00, 0xFF} // payload_len=255, но данных 0
	_, _, err = f.Decode(bad)
	if err == nil {
		t.Fatal("ожидалась ошибка для payload_len > размера")
	}

	// Неизвестный flags
	unknown := []byte{0x55, 0x00, 0x00}
	_, _, err = f.Decode(unknown)
	if err == nil {
		t.Fatal("ожидалась ошибка для неизвестного флага")
	}
}

func TestFramerDecodeDummy(t *testing.T) {
	f := &Framer{}
	// Dummy: flags=0xA2, payload_len=0, затем padding
	frame := f.Encode(FlagDummy, nil, 100)
	flags, payload, err := f.Decode(frame)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	if flags != FlagDummy {
		t.Fatalf("flags: got 0x%02x, want 0x%02x", flags, FlagDummy)
	}
	if len(payload) != 0 {
		t.Fatalf("dummy payload len: got %d, want 0", len(payload))
	}
}

func TestIsStealthPacket(t *testing.T) {
	// Stealth flags
	for _, b := range []byte{0xA1, 0xA2, 0xA3} {
		if !IsStealthPacket(b) {
			t.Errorf("IsStealthPacket(0x%02x) = false, want true", b)
		}
	}
	// WireGuard message types
	for _, b := range []byte{0x01, 0x02, 0x03, 0x04} {
		if IsStealthPacket(b) {
			t.Errorf("IsStealthPacket(0x%02x) = true, want false", b)
		}
	}
}

func TestFramerVersionInfoRoundtrip(t *testing.T) {
	f := &Framer{}
	cfg := VersionConfig{Version: 1, PacingMode: PacingVideo}
	encoded := f.EncodeVersionInfo(cfg)
	decoded, ok := f.DecodeVersionInfo(encoded)
	if !ok {
		t.Fatal("DecodeVersionInfo вернул false")
	}
	if decoded.Version != 1 {
		t.Fatalf("version: got %d, want 1", decoded.Version)
	}
	if decoded.PacingMode != PacingVideo {
		t.Fatalf("pacing: got %q, want %q", decoded.PacingMode, PacingVideo)
	}
}

func TestFramerVersionInfoInvalid(t *testing.T) {
	f := &Framer{}
	// Слишком короткий payload
	_, ok := f.DecodeVersionInfo([]byte{0x01})
	if ok {
		t.Fatal("DecodeVersionInfo должен вернуть false для короткого payload")
	}
	// nil
	_, ok = f.DecodeVersionInfo(nil)
	if ok {
		t.Fatal("DecodeVersionInfo должен вернуть false для nil")
	}
}

func TestFramerVersionInfoInDataPacket(t *testing.T) {
	f := &Framer{}
	cfg := VersionConfig{Version: 1, PacingMode: PacingMixed}
	versionPayload := f.EncodeVersionInfo(cfg)

	// Реальные данные после version info
	realData := []byte("wireguard handshake init")
	fullPayload := append(versionPayload, realData...)

	frame := f.Encode(FlagData, fullPayload, 1000)
	flags, payload, err := f.Decode(frame)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	if flags != FlagData {
		t.Fatalf("flags: got 0x%02x", flags)
	}

	decoded, ok := f.DecodeVersionInfo(payload)
	if !ok {
		t.Fatal("DecodeVersionInfo вернул false")
	}
	if decoded.PacingMode != PacingMixed {
		t.Fatalf("pacing: got %q", decoded.PacingMode)
	}

	// Данные после version info
	remaining := payload[versionInfoSize:]
	if string(remaining) != string(realData) {
		t.Fatalf("remaining: got %q, want %q", remaining, realData)
	}
}
