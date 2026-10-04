// Package secretfile reads a secret setting either from its variable or from a
// file, so production can mount secrets as files (the Secret Manager add-on)
// instead of putting them in the environment (docs/design/28-production.md).
package secretfile

import (
	"fmt"
	"os"
	"strings"
)

// FileSuffix turns a setting's key into the key of its file form:
// api_server_keys is api_server_keys_file (API_SERVER_KEYS_FILE).
const FileSuffix = "_file"

// Resolve returns the setting key, read with get, which takes lowercase keys the
// way viper.GetString does ("redis_url" for REDIS_URL). When key+[FileSuffix]
// names a file, the value is that file's contents with surrounding whitespace
// (a trailing newline, say) trimmed. Setting both forms, or naming a file that
// can't be read, is an error. Errors name the variables and the file, never the
// value.
func Resolve(get func(key string) string, key string) (string, error) {
	fileKey := key + FileSuffix
	value := strings.TrimSpace(get(key))
	path := strings.TrimSpace(get(fileKey))
	switch {
	case path == "":
		return value, nil
	case value != "":
		return "", fmt.Errorf("set %s or %s, not both", strings.ToUpper(key), strings.ToUpper(fileKey))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", strings.ToUpper(fileKey), err)
	}
	return strings.TrimSpace(string(data)), nil
}
