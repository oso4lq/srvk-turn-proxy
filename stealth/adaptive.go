// stealth/adaptive.go

package stealth

// PacerMetrics — snapshot утилизации буфера Pacer.
// Приблизительный: между чтением отдельных каналов значения могут измениться.
// Допустимо для threshold-решений Advisor.
type PacerMetrics struct {
	BufLen int // текущее заполнение
	BufCap int // ёмкость
}

// ScaleAdvice — рекомендация Advisor по масштабированию каналов.
type ScaleAdvice int

const (
	Hold      ScaleAdvice = 0
	ScaleUp   ScaleAdvice = 1
	ScaleDown ScaleAdvice = -1
)

// AdvisorConfig — конфигурация Advisor.
type AdvisorConfig struct {
	HighMark    float64 // порог утилизации для scale-up (default 0.70)
	LowMark     float64 // порог для scale-down (default 0.20)
	StableCount int     // тиков подряд для принятия решения (default 3)
}

// DefaultAdvisorConfig возвращает конфигурацию по умолчанию.
func DefaultAdvisorConfig() AdvisorConfig {
	return AdvisorConfig{
		HighMark:    0.70,
		LowMark:     0.20,
		StableCount: 3,
	}
}

// Advisor — калькулятор масштабирования. Pull-модель: вызывающий
// вызывает Tick() когда считает нужным, получает ScaleAdvice.
// Без горутин, без I/O. Тестируется тривиально.
type Advisor struct {
	cfg        AdvisorConfig
	highStreak int
	lowStreak  int
}

// NewAdvisor создаёт Advisor.
func NewAdvisor(cfg AdvisorConfig) *Advisor {
	return &Advisor{cfg: cfg}
}

// Tick принимает метрики всех активных Pacer'ов и возвращает ScaleAdvice.
// Агрегирует утилизацию, пропускает каналы с BufCap==0 (ещё не запущены).
func (a *Advisor) Tick(metrics []PacerMetrics) ScaleAdvice {
	var totalLen, totalCap int
	for _, m := range metrics {
		if m.BufCap == 0 {
			continue
		}
		totalLen += m.BufLen
		totalCap += m.BufCap
	}

	if totalCap == 0 {
		return Hold
	}

	utilization := float64(totalLen) / float64(totalCap)

	switch {
	case utilization > a.cfg.HighMark:
		a.highStreak++
		a.lowStreak = 0
	case utilization < a.cfg.LowMark:
		a.lowStreak++
		a.highStreak = 0
	default:
		a.highStreak = 0
		a.lowStreak = 0
	}

	if a.highStreak >= a.cfg.StableCount {
		a.highStreak = 0
		return ScaleUp
	}
	if a.lowStreak >= a.cfg.StableCount {
		a.lowStreak = 0
		return ScaleDown
	}

	return Hold
}
