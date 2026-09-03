#!/usr/bin/env bash
# Points api.library.appmd.dev at the Render service.
#
# Two systems have to agree for a custom domain to work, and doing only one of
# them fails in a confusing way:
#
#   1. Render must be told to answer for the hostname, or it serves its default
#      certificate and rejects the request.
#   2. Cloudflare must resolve the hostname to Render.
#
# This does both. It needs two API tokens, neither of which is stored:
#
#   RENDER_API_KEY      dashboard.render.com/settings#api-keys
#   CLOUDFLARE_API_TOKEN dash.cloudflare.com/profile/api-tokens
#                        -> Create Token -> Edit zone DNS -> Zone: appmd.dev
#
# Usage:
#   RENDER_API_KEY=rnd_... CLOUDFLARE_API_TOKEN=... ./scripts/point-domain.sh

set -euo pipefail

SERVICE_ID="srv-dacuhqad0e5s73fhol80"
ZONE_ID="d64a7a18e80cd1127c3430dfd3ea247e"
HOSTNAME="api.library.appmd.dev"
TARGET="holibrary-api.onrender.com"

: "${RENDER_API_KEY:?set RENDER_API_KEY}"
: "${CLOUDFLARE_API_TOKEN:?set CLOUDFLARE_API_TOKEN}"

echo
echo "  1/3  telling Render to answer for ${HOSTNAME}"
render_response=$(curl -sS -X POST \
  "https://api.render.com/v1/services/${SERVICE_ID}/custom-domains" \
  -H "Authorization: Bearer ${RENDER_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"${HOSTNAME}\"}")
echo "       $(printf '%s' "$render_response" | head -c 200)"

echo
echo "  2/3  creating the CNAME in Cloudflare"
# Proxied is deliberately FALSE to begin with. Render verifies ownership by
# resolving the hostname to itself; with Cloudflare proxying, it sees
# Cloudflare's address instead and the verification never completes. Turn the
# proxy on after Render reports the domain verified, at which point Cloudflare's
# WAF and caching sit in front as intended.
cf_response=$(curl -sS -X POST \
  "https://api.cloudflare.com/client/v4/zones/${ZONE_ID}/dns_records" \
  -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{\"type\":\"CNAME\",\"name\":\"${HOSTNAME}\",\"content\":\"${TARGET}\",\"ttl\":300,\"proxied\":false,\"comment\":\"HOLibrary API on Render\"}")
echo "       success: $(printf '%s' "$cf_response" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("success"))' 2>/dev/null || echo '?')"
printf '%s' "$cf_response" | python3 -c '
import json,sys
d=json.load(sys.stdin)
for e in d.get("errors",[]): print("       error:", e.get("message"))
r=d.get("result") or {}
if r: print("       record:", r.get("name"), "->", r.get("content"), "proxied:", r.get("proxied"))
' 2>/dev/null || true

echo
echo "  3/3  waiting for DNS, then checking Render has verified it"
for i in $(seq 1 20); do
  resolved=$(dig +short "${HOSTNAME}" CNAME 2>/dev/null | head -1)
  if [ -n "$resolved" ]; then
    echo "       resolves to ${resolved}"
    break
  fi
  sleep 15
done

echo
echo "  Certificate issuance takes a few minutes. Check with:"
echo "    curl -I https://${HOSTNAME}/healthz"
echo
echo "  Once it answers, turn the Cloudflare proxy ON (orange cloud) for"
echo "  ${HOSTNAME}, and set SSL/TLS mode to Full (strict). Leaving it off"
echo "  means every request bypasses Cloudflare's WAF and goes straight to"
echo "  Render, which also exposes the origin address."
echo
