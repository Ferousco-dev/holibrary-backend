#!/usr/bin/env bash
# Changes an account's password against the deployed API.
#
# Passwords are read from a prompt rather than taken as arguments, so neither
# the old nor the new one ends up in the shell history or in the process list.
#
# Usage:
#   ./scripts/change-password.sh [email] [api-url]
set -euo pipefail

EMAIL="${1:-feranmioresajo@gmail.com}"
API="${2:-https://api.library.appmd.dev}"

echo
echo "  account: $EMAIL"
echo "  api:     $API"
echo

read -rsp "  current password: " CURRENT; echo
read -rsp "  new password:     " NEW; echo
read -rsp "  new password again: " CONFIRM; echo
echo

if [ "$NEW" != "$CONFIRM" ]; then
  echo "  the two new passwords do not match"; exit 1
fi
# The API enforces this too, but failing here saves a round trip and a
# confusing error.
if [ ${#NEW} -lt 10 ]; then
  echo "  the new password must be at least 10 characters"; exit 1
fi

login() {
  curl -s -m 45 -X POST "$API/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -A 'HOLibrary-Setup/1.0' \
    -d "$(python3 -c '
import json,sys
print(json.dumps({"login": sys.argv[1], "password": sys.argv[2]}))' "$EMAIL" "$1")"
}

echo "  signing in..."
RESPONSE=$(login "$CURRENT")
TOKEN=$(printf '%s' "$RESPONSE" | python3 -c '
import json,sys
d=json.load(sys.stdin)
if "error" in d:
    print("ERROR:" + d["error"]["code"] + ":" + d["error"]["message"]); sys.exit(0)
print(d["data"]["access_token"])' 2>/dev/null || echo "ERROR:PARSE:unreadable response")

case "$TOKEN" in
  ERROR:*)
    echo "  could not sign in: ${TOKEN#ERROR:}"
    echo
    echo "  If it says INVALID_CREDENTIALS the current password is wrong."
    echo "  If it says RATE_LIMITED, wait a minute: five attempts per account."
    exit 1;;
esac
echo "  signed in."

echo "  changing the password..."
RESULT=$(curl -s -m 45 -X POST "$API/api/v1/auth/change-password" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -A 'HOLibrary-Setup/1.0' \
  -d "$(python3 -c '
import json,sys
print(json.dumps({"current_password": sys.argv[1], "new_password": sys.argv[2]}))' "$CURRENT" "$NEW")")

printf '%s' "$RESULT" | python3 -c '
import json,sys
d=json.load(sys.stdin)
if "error" in d:
    print("  failed:", d["error"]["code"], "-", d["error"]["message"]); sys.exit(1)
print("  changed.")'

# Prove it, rather than assuming. Changing a password also invalidates every
# existing session, so the old token is expected to be dead now.
echo "  verifying..."
VERIFY=$(login "$NEW")
printf '%s' "$VERIFY" | python3 -c '
import json,sys
d=json.load(sys.stdin)
if "error" in d:
    print("  the new password was NOT accepted:", d["error"]["code"]); sys.exit(1)
print("  the new password works. must_change_password =", d["data"]["must_change_password"])'

OLD_TOKEN_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -m 45 "$API/api/v1/me/loans" \
  -H "Authorization: Bearer $TOKEN" -A 'HOLibrary-Setup/1.0')
echo "  the old session is now: HTTP $OLD_TOKEN_STATUS (401 expected: a password change ends every session)"
echo
echo "  Now delete the temporary password file:"
echo "    rm ~/Desktop/SCHOOL/secrets/holibrary-admin-password.txt"
echo
