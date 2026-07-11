package manage

import (
	"crypto/rand"
	"math/big"
)

const tokenCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randomToken returns a cryptographically-random token of the given length
// using upper/lowercase letters and digits.
func randomToken(length int) string {
	b := make([]byte, length)
	max := big.NewInt(int64(len(tokenCharset)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// Extremely unlikely; fall back to a fixed but valid character.
			b[i] = tokenCharset[i%len(tokenCharset)]
			continue
		}
		b[i] = tokenCharset[n.Int64()]
	}
	return string(b)
}
