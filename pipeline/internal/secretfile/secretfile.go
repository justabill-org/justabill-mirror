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
// congress_api_key is congress_api_key_file (CONGRESS_API_KEY_FILE).
const FileSuffix = "_file"

// Resolve returns the setting key, read with get, which takes lowercase keys the
// way viper.GetString does ("congress_api_key" for CONGRESS_API_KEY). When key+[FileSuffix]
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
