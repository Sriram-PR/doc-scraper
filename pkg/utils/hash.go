package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// CalculateFileMD5 computes the MD5 hash of a file's content.
func CalculateFileMD5(filePath string) (string, error) {
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

// CalculateStringMD5 computes the MD5 hash of a string.
func CalculateStringMD5(content string) string {
	hash := sha256.New()
	hash.Write([]byte(content))
	return hex.EncodeToString(hash.Sum(nil))
}
