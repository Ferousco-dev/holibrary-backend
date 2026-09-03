# Risk Register

| ID | Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|---|
| RSK-001 | Render free tier cold start (~30-60s) stalls the live demo | High | High | DEC-013 keep-warm during defence window; warm the URL 3 min before presenting | Student |
| RSK-002 | Resend cannot email students until a sending domain is DNS-verified. Blocks password reset (REQ-004, REQ-005) | High | Medium | Acquire domain and verify via Cloudflare DNS before demo | Student |
| RSK-003 | Stack (Go, Docker, Redis, FCM, CI/CD) far exceeds the taught SEN 106 syllabus, which currently ends at CSS Flexbox. Examiner may read it as unexplained complexity | Medium | High | Every technology choice carries a DEC record naming the problem solved and the alternative rejected | Student |
| RSK-004 | One-day build target for the full feature set | High | Medium | Trimmed scope: must-have core tonight, reservations/notifications next | Conductor |
| RSK-005 | Borrowing history is a reading-habits profile of named students. Leak causes real privacy harm | Low | High | RBAC enforced server-side; members read only their own history; no PII in logs (NFR-004, NFR-010) | Constructor |
| RSK-006 | The Redis instance is shared with another application (Orastudy). A `FLUSHALL` by either would clear the other's rate-limit counters | Low | Low | Keys namespaced `holibrary:`; neither application issues `FLUSHALL`; losing counters costs limits for one window, not data | Student |
| RSK-007 | Render's free tier sleeps after 15 minutes idle; the first request then takes 30-60 seconds. During a live defence this reads as a broken system | High | High | Warm the URL before presenting; enable the keep-warm schedule for the defence window only (DEC-013) | Student |
| RSK-008 | `library.appmd.dev` serves no TLS: the frontend is not yet attached to that hostname in Vercel. `CORS_ORIGINS` and every email link already point at it | High | Medium | Attach the domain in Vercel before the demo. If Vercel's certificate verification fails, set the record unproxied until it verifies, as was done for the API | Frontend |
| RSK-009 | The bootstrap password is shown once and stored only as an Argon2 hash. It was lost twice during setup, once because an error went to stderr while `tee` watched stdout | Medium | Low | `scripts/reset-admin.sh` deletes the administrator so bootstrap can run again; it refuses once the deployment has members or loans | Student |
