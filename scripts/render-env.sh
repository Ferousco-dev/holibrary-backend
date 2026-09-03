#!/usr/bin/env bash
# Prints the environment variables to set on the Render service.
#
# Values are printed to YOUR terminal only. Nothing is sent anywhere, and the
# Resend key is never handled by this script: you paste that into Render
# yourself, so it exists in exactly one place.
set -u

echo
echo "  Render dashboard -> holibrary-api -> Environment"
echo "  https://dashboard.render.com/web/srv-dacuhqad0e5s73fhol80/env"
echo
echo "  Add these two:"
echo
echo "    REDIS_URL"
cat /tmp/upstash_url 2>/dev/null | sed 's/^/      /' || echo "      (regenerate: upstash redis get --db-id d8f96c08-463c-48ba-9851-ac14da72be37)"
echo
echo "    REDIS_PREFIX"
echo "      holibrary"
echo
echo "  And your Resend key, which I have never seen and do not want to:"
echo
echo "    RESEND_API_KEY"
echo "      re_...   (from resend.com/api-keys)"
echo
echo "  Already set, leave them alone:"
echo "    ENV, DATABASE_URL, JWT_SECRET, MAIL_FROM,"
echo "    TRUST_PROXY_HEADERS, SEED_DEMO_DATA, OPENLIBRARY_BASE_URL"
echo
echo "  Render redeploys automatically when you save."
echo
