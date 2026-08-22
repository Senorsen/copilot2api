// Package securetoken hashes tokens and compares their fixed-length digests in constant time.
package securetoken

import (
	"crypto/sha256"
	"crypto/subtle"
)

// Digest is a fixed-length SHA-256 token digest.
type Digest [sha256.Size]byte

// Hash returns the SHA-256 digest of a token.
func Hash(token string) Digest {
	return sha256.Sum256([]byte(token))
}

// Equal compares two token digests in constant time.
func Equal(a, b Digest) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// Matches hashes a candidate token and compares it with the digest.
func (d Digest) Matches(candidate string) bool {
	return Equal(d, Hash(candidate))
}
