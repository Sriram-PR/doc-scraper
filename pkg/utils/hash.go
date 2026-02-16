package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// CalculateFileSHA256 computes the SHA-256 hash of a file's content.
func CalculateFileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// CalculateStringSHA256 computes the SHA-256 hash of a string.
func CalculateStringSHA256(content string) string {
	hash := sha256.New()
	hash.Write([]byte(content))
	return hex.EncodeToString(hash.Sum(nil))
}
