package auth

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	mathrand "math/rand"
)

const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GenerateKey generates a cryptographically secure random API key.
// Format: "pk-" followed by 48 random base62 characters.
func GenerateKey() string {
	const length = 48
	result := make([]byte, length)
	alphabetLen := big.NewInt(int64(len(base62Chars)))
	for i := range result {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			result[i] = base62Chars[mathrand.Intn(len(base62Chars))]
		} else {
			result[i] = base62Chars[n.Int64()]
		}
	}
	return "pk-" + string(result)
}

// GenerateID generates a short unique ID consisting of 16 lowercase hex characters.
func GenerateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback if crypto/rand fails. In Go 1.20+, the global math/rand is
		// automatically seeded.
		mathrand.Read(b)
	}
	return hex.EncodeToString(b)
}
