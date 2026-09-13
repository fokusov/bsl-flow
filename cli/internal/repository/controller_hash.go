package repository

import (
	"crypto/sha256"
	"encoding/hex"
)

func fileSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
