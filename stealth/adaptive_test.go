// stealth/adaptive_test.go

package stealth

import "testing"

func TestAdvisor_ScaleUp(t *testing.T) {
	adv := NewAdvisor(DefaultAdvisorConfig())
	high := []PacerMetrics{{BufLen: 50, BufCap: 64}} // 78% > 70%

	// Первые 2 тика — Hold (streak < StableCount)
	for i := 0; i < 2; i++ {
		if got := adv.Tick(high); got != Hold {
			t.Fatalf("tick %d: got %d, want Hold", i, got)
		}
	}
	// Третий тик — ScaleUp
	if got := adv.Tick(high); got != ScaleUp {
		t.Fatalf("tick 2: got %d, want ScaleUp", got)
	}
}

func TestAdvisor_ScaleDown(t *testing.T) {
	adv := NewAdvisor(DefaultAdvisorConfig())
	low := []PacerMetrics{{BufLen: 5, BufCap: 64}} // 7.8% < 20%

	for i := 0; i < 2; i++ {
		if got := adv.Tick(low); got != Hold {
			t.Fatalf("tick %d: got %d, want Hold", i, got)
		}
	}
	if got := adv.Tick(low); got != ScaleDown {
		t.Fatalf("tick 2: got %d, want ScaleDown", got)
	}
}

func TestAdvisor_Hold(t *testing.T) {
	adv := NewAdvisor(DefaultAdvisorConfig())
	mid := []PacerMetrics{{BufLen: 30, BufCap: 64}} // 46.9% — между порогами

	for i := 0; i < 10; i++ {
		if got := adv.Tick(mid); got != Hold {
			t.Fatalf("tick %d: got %d, want Hold", i, got)
		}
	}
}

func TestAdvisor_StreakReset(t *testing.T) {
	adv := NewAdvisor(DefaultAdvisorConfig())
	high := []PacerMetrics{{BufLen: 50, BufCap: 64}}
	mid := []PacerMetrics{{BufLen: 30, BufCap: 64}}

	// 2 high, потом mid — streak сбрасывается
	adv.Tick(high)
	adv.Tick(high)
	adv.Tick(mid) // сброс
	// Снова 2 high — ещё не хватает
	adv.Tick(high)
	if got := adv.Tick(high); got != Hold {
		t.Fatalf("got %d, want Hold (streak был сброшен)", got)
	}
	// Третий high подряд — ScaleUp
	if got := adv.Tick(high); got != ScaleUp {
		t.Fatalf("got %d, want ScaleUp", got)
	}
}

func TestAdvisor_EmptyMetrics(t *testing.T) {
	adv := NewAdvisor(DefaultAdvisorConfig())
	for i := 0; i < 5; i++ {
		if got := adv.Tick(nil); got != Hold {
			t.Fatalf("tick %d: got %d, want Hold", i, got)
		}
	}
}

func TestAdvisor_ZeroCapSkipped(t *testing.T) {
	adv := NewAdvisor(DefaultAdvisorConfig())
	// Один канал с cap=0 (Pacer ещё не запущен), один с высокой нагрузкой
	metrics := []PacerMetrics{
		{BufLen: 0, BufCap: 0},   // пропускается
		{BufLen: 50, BufCap: 64}, // 78%
	}
	for i := 0; i < 2; i++ {
		adv.Tick(metrics)
	}
	if got := adv.Tick(metrics); got != ScaleUp {
		t.Fatalf("got %d, want ScaleUp (zero-cap пропущен)", got)
	}
}

func TestAdvisor_StreakResetAfterAdvice(t *testing.T) {
	adv := NewAdvisor(DefaultAdvisorConfig())
	high := []PacerMetrics{{BufLen: 50, BufCap: 64}}

	// 3 тика → ScaleUp, streak обнуляется
	adv.Tick(high)
	adv.Tick(high)
	adv.Tick(high) // ScaleUp

	// Следующий тик — Hold (streak = 1, не 4)
	if got := adv.Tick(high); got != Hold {
		t.Fatalf("got %d, want Hold (streak сброшен после ScaleUp)", got)
	}
}
