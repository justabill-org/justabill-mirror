#!/bin/sh
# Signs in to the Firebase Auth emulator as a fake Google user and prints the ID token.
#
#   dev/auth-emulator/mint-token.sh [subject]
#
# The same subject always maps to the same emulator uid, so repeated calls sign in the same
# person. Emulator tokens are unsigned (alg "none"); production never accepts them (#135).
set -eu

sub="${1:-local-dev}"
case "$sub" in
  *[!A-Za-z0-9_-]* | "")
    echo "subject must match [A-Za-z0-9_-]+" >&2
    exit 2
    ;;
esac

host="${FIREBASE_AUTH_EMULATOR_HOST:-localhost:9099}"
url="http://${host}/identitytoolkit.googleapis.com/v1/accounts:signInWithIdp?key=fake-api-key"
# The emulator's fake IdP takes the provider's claims as a URL-encoded JSON id_token:
# {"sub":"<sub>","email":"<sub>@example.com","email_verified":true}
claims="%7B%22sub%22%3A%22${sub}%22%2C%22email%22%3A%22${sub}%40example.com%22%2C%22email_verified%22%3Atrue%7D"
body="{\"postBody\":\"providerId=google.com&id_token=${claims}\",\"requestUri\":\"http://localhost\",\"returnSecureToken\":true}"

if ! resp="$(curl -sS --fail-with-body -H 'Content-Type: application/json' -d "$body" "$url")"; then
  echo "is the Auth emulator running at ${host}? Start it with: task infra:up" >&2
  exit 1
fi
token="$(printf '%s\n' "$resp" | sed -n 's/.*"idToken": *"\([^"]*\)".*/\1/p')"
if [ -z "$token" ]; then
  echo "no idToken in the emulator's response:" >&2
  printf '%s\n' "$resp" >&2
  exit 1
fi
printf '%s\n' "$token"
