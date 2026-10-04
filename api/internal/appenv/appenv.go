// Package appenv parses APP_ENV, which says whether the API runs in
// production. Production turns on the fail-closed checks.
package appenv

import "fmt"

// Env is a deployment environment.
type Env string

// The environments APP_ENV accepts.
const (
	Development Env = "development"
	Production  Env = "production"
)

// Parse reads APP_ENV. Empty means development. Anything else unknown is an
// error rather than a silent development default, so a typo like "prod"
// can't switch the production checks off.
func Parse(s string) (Env, error) {
	switch Env(s) {
	case "", Development:
		return Development, nil
	case Production:
		return Production, nil
	default:
		return "", fmt.Errorf("APP_ENV=%q: want %q or %q", s, Development, Production)
	}
}
