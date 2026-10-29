package outbox

import (
	"math/rand/v2"
	"time"
)

const maxBackoff = 10 * time.Minute

func Backoff(attempt int, base time.Duration) time.Duration {
	delay := maxBackoff
	if shift := attempt - 1; shift >= 0 && shift < 20 {
		if scaled := base << shift; scaled > 0 && scaled < maxBackoff {
			delay = scaled
		}
	}
	half := delay / 2
	return half + rand.N(half+1)
}
