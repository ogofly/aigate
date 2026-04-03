package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func KeyID(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}
