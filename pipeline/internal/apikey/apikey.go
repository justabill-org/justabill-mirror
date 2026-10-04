// Package apikey names the header api.data.gov keys (Congress.gov, GovInfo) travel in, instead
// of a query parameter, so request URLs, and the errors that quote them, never contain a key.
// The upstream client sets it per attempt, for the key's own host only.
package apikey

// Header is the api.data.gov key header, accepted by both Congress.gov and GovInfo.
const Header = "X-Api-Key"
