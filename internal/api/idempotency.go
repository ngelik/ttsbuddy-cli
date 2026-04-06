package api

import (
	"crypto/sha256"
	"fmt"

	"crypto/rand"
)

// GenerateFromContent produces a deterministic idempotency key from input parameters.
// Same text + voice + speed always produces the same key.
func GenerateFromContent(text, voice string, speed float64) string {
	h := sha256.New()
	// Use full float precision to avoid collapsing distinct speed values
	// (e.g., 1.004 and 1.005 must produce different keys).
	fmt.Fprintf(h, "%s|%s|%v", text, voice, speed)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// GenerateFromStdin produces a random UUID for stdin input (not deterministic).
func GenerateFromStdin() string {
	return generateUUID()
}

// GenerateNew produces a fresh UUID for retry with a new idempotency key.
func GenerateNew() string {
	return generateUUID()
}

func generateUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: this should never happen
		panic("crypto/rand failed: " + err.Error())
	}
	// Set UUID version 4 and variant bits
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
