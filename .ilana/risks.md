# Risk Register

| ID | Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|---|
| RSK-001 | Render free tier cold start (~30-60s) stalls the live demo | High | High | DEC-013 keep-warm during defence window; warm the URL 3 min before presenting | Student |
| RSK-002 | Resend cannot email students until a sending domain is DNS-verified. Blocks password reset (REQ-004, REQ-005) | High | Medium | Acquire domain and verify via Cloudflare DNS before demo | Student |
| RSK-003 | Stack (Go, Docker, Redis, FCM, CI/CD) far exceeds the taught SEN 106 syllabus, which currently ends at CSS Flexbox. Examiner may read it as unexplained complexity | Medium | High | Every technology choice carries a DEC record naming the problem solved and the alternative rejected | Student |
| RSK-004 | One-day build target for the full feature set | High | Medium | Trimmed scope: must-have core tonight, reservations/notifications next | Conductor |
| RSK-005 | Borrowing history is a reading-habits profile of named students. Leak causes real privacy harm | Low | High | RBAC enforced server-side; members read only their own history; no PII in logs (NFR-004, NFR-010) | Constructor |
