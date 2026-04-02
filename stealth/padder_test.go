package stealth

import (
	"testing"
)

func TestPadderSmallPayloadAudioProfile(t *testing.T) {
	p := NewPadder(DefaultPadderConfig())
	for i := 0; i < 100; i++ {
		target := p.TargetSize(50) // маленький — AudioProfile
		if target < AudioProfile.Min || target > AudioProfile.Max+5 {
			t.Fatalf("target %d вне диапазона audio [%d, %d+5]", target, AudioProfile.Min, AudioProfile.Max)
		}
	}
}

func TestPadderLargePayloadVideoProfile(t *testing.T) {
	p := NewPadder(DefaultPadderConfig())
	for i := 0; i < 100; i++ {
		target := p.TargetSize(500) // большой — VideoProfile
		if target < VideoProfile.Min || target > VideoProfile.Max+5 {
			t.Fatalf("target %d вне диапазона video [%d, %d+5]", target, VideoProfile.Min, VideoProfile.Max)
		}
	}
}

func TestPadderBoundaryPayload(t *testing.T) {
	p := NewPadder(DefaultPadderConfig())
	// payload=197 — граница, AudioProfile
	prof := p.Profile(197)
	if prof != AudioProfile {
		t.Fatalf("payload 197: got VideoProfile, want AudioProfile")
	}
	// payload=198 — VideoProfile
	prof = p.Profile(198)
	if prof != VideoProfile {
		t.Fatalf("payload 198: got AudioProfile, want VideoProfile")
	}
}

func TestPadderOversizePayload(t *testing.T) {
	p := NewPadder(DefaultPadderConfig())
	// payload > VideoProfile.Max — без padding
	target := p.TargetSize(1500)
	expected := 1500 + headerSize
	if target != expected {
		t.Fatalf("oversize target: got %d, want %d", target, expected)
	}
}

func TestPadderPayloadExceedsProfileMax(t *testing.T) {
	p := NewPadder(DefaultPadderConfig())
	// payload=199 → AudioProfile но payload+3=202 > AudioProfile.Max=200
	// Должен перейти в VideoProfile или отдать payload+3
	target := p.TargetSize(199)
	// 199+3=202 > AudioProfile.Max(200), значит VideoProfile (Min=800)
	// target должен быть >= 800
	if target < VideoProfile.Min {
		t.Fatalf("payload 199: target %d < VideoProfile.Min %d", target, VideoProfile.Min)
	}
}

func TestPadderNonDeterministic(t *testing.T) {
	p := NewPadder(DefaultPadderConfig())
	seen := make(map[int]bool)
	for i := 0; i < 1000; i++ {
		seen[p.TargetSize(100)] = true
	}
	if len(seen) < 3 {
		t.Fatalf("TargetSize слишком детерминирован: %d уникальных значений из 1000", len(seen))
	}
}

func TestPadderProfileDistribution(t *testing.T) {
	p := NewPadder(DefaultPadderConfig())
	for i := 0; i < 1000; i++ {
		target := p.TargetSize(100) // AudioProfile
		minAllowed := AudioProfile.Min - 5
		maxAllowed := AudioProfile.Max + 5
		if target < minAllowed || target > maxAllowed {
			t.Fatalf("target %d вне [%d, %d] (профиль ±5)", target, minAllowed, maxAllowed)
		}
	}
}
