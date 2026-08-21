package utils

import (
	"crypto/sha256"
	"encoding/hex"
)

// CalculateStringSHA256 computes the SHA-256 hash of a string.
func CalculateStringSHA256(content string) string {
	hash := sha256.New()
	hash.Write([]byte(content))
	return hex.EncodeToString(hash.Sum(nil))
}
