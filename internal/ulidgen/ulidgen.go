package ulidgen

import (
	"crypto/rand"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	mu      sync.Mutex
	entropy *ulid.MonotonicEntropy
)

func init() {
	entropy = ulid.Monotonic(rand.Reader, 0)
}

func New() string {
	mu.Lock()
	defer mu.Unlock()
	id := ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
	return id.String()
}

func WithPrefix(prefix string) string {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("_")
	b.WriteString(New())
	return b.String()
}
