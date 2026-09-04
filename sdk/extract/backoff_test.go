package extract

import (
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestBackoffSemJitterNaoEntraEmPanico: rand.Int63n entra em panico com
// argumento nao-positivo, entao um RetryConfig{MaxAttempts: 5} e mais nada --
// que e uma coisa razoavel de se escrever -- derrubava o processo no primeiro
// retry. Nao ter jitter e uma escolha, nao um erro.
func TestBackoffSemJitterNaoEntraEmPanico(t *testing.T) {
	casos := []struct {
		nome string
		cfg  core.RetryConfig
	}{
		{"so MaxAttempts", core.RetryConfig{MaxAttempts: 5}},
		{"sem jitter", core.RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Second}},
		{"sem MaxBackoff", core.RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, JitterFraction: 0.1}},
		{"zerado", core.RetryConfig{}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			for attempt := 0; attempt < 4; attempt++ {
				if d := calculateBackoff(attempt, &c.cfg); d < 0 {
					t.Errorf("backoff negativo na tentativa %d: %v", attempt, d)
				}
			}
		})
	}
}

// TestBackoffSemMaxBackoffNaoTravaEmZero: com MaxBackoff zerado o teto era
// zero, e todo backoff era truncado para nada -- um retry imediato em loop
// contra uma API que acabou de devolver 429.
func TestBackoffSemMaxBackoffNaoTravaEmZero(t *testing.T) {
	cfg := core.RetryConfig{MaxAttempts: 3, InitialBackoff: 100 * time.Millisecond}
	if d := calculateBackoff(1, &cfg); d < 200*time.Millisecond {
		t.Errorf("backoff = %v, esperado ao menos 200ms (2^1 x 100ms)", d)
	}
}
