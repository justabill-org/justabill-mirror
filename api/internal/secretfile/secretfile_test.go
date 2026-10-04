package secretfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/api/internal/secretfile"
)

// settings is a get function over a map.
func settings(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "key")
	if err := os.WriteFile(file, []byte("  from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr string
	}{
		{name: "unset", env: nil, want: ""},
		{name: "variable", env: map[string]string{"api_key": " from-env "}, want: "from-env"},
		{name: "file", env: map[string]string{"api_key_file": file}, want: "from-file"},
		{
			name:    "both",
			env:     map[string]string{"api_key": "from-env", "api_key_file": file},
			wantErr: "set API_KEY or API_KEY_FILE, not both",
		},
		{
			name:    "missing file",
			env:     map[string]string{"api_key_file": filepath.Join(dir, "missing")},
			wantErr: "read API_KEY_FILE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := secretfile.Resolve(settings(tt.env), "api_key")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Resolve() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("Resolve() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestResolveNeverQuotesTheValue(t *testing.T) {
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := secretfile.Resolve(settings(map[string]string{"k": "hunter2-secret", "k_file": file}), "k")
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("Resolve() error = %v: want an error that doesn't quote the value", err)
	}
}
