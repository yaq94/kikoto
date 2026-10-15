package openlist

import (
	"encoding/hex"
	"encoding/json"
	"strings"
)

// ContentHash accepts an explicit SHA-256 digest from OpenList's hash_info map.
// Names, timestamps, sizes, signatures and legacy untyped strings are not proof.
// Conflicting aliases or malformed evidence disable automatic recognition.
func (o Object) ContentHash() string {
	if len(o.HashInfo) > 2048 {
		return ""
	}
	var hashes map[string]string
	if json.Unmarshal(o.HashInfo, &hashes) != nil {
		return ""
	}
	result := ""
	for algorithm, value := range hashes {
		if strings.ReplaceAll(strings.ToLower(algorithm), "-", "") != "sha256" {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 || (result != "" && result != "sha256:"+value) {
			return ""
		}
		result = "sha256:" + value
	}
	return result
}
