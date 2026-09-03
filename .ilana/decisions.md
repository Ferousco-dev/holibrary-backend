# Decision Records

| ID | Decision | Rationale |
|---|---|---|
| DEC-001 | Backend language: **Go** | Cloudflare's container limitation applies to any language, so it is not a reason to switch. Go suits the last-copy concurrency problem, produces a small static binary, and yields a tiny Docker image. |
| DEC-002 | Model non-borrowable copies (reference / on-display) | LIB 001 material: Reference collection is consulted in-room; Recent Accessions "may not be borrowed while on display". A system that lends a dictionary is wrong about HOL. |
| DEC-003 | Reservations / holds **in scope** | LIB 001: on-display books "may be reserved at the Loans desk". The real library does this. |
| DEC-004 | Member categories: undergraduate / postgraduate / staff | LIB 001: serials back-file access differs by category. Privileges are not uniform at HOL. |
| DEC-005 | Loan period and limit **per member category** | undergraduate 2/14d, postgraduate 4/21d, staff 6/28d. |
| DEC-006 | **No self-registration.** Librarian-created accounts + CSV import | Mirrors HOL: users "must present the university identity and library cards and sign the register". Also the more secure design. |
| DEC-007 | External book API seeds bibliographic data only | Africana and OAU Publications are absent from public book APIs. Librarian manual/CSV entry stays authoritative. |
| DEC-008 | Two repositories (backend, frontend). Backend first. | Owned by the student's account. **No AI co-author or contributor trailer on any commit.** |
| DEC-009 | Renewals **deferred to v2** | Not in the project brief. Deferred, not dropped. |
| DEC-010 | Deployment: Render (Docker) + Neon Postgres + Upstash Redis + Cloudflare edge + GitHub Actions | All free with no trial clock. Fly.io and Koyeb no longer offer standing free compute (verified Sept 2026). Cloudflare sits in front as edge, which is how Cloudflare is used in production. |
| DEC-011 | Neon for Postgres rather than Render's free Postgres | Render's free database expires after ~30 days; Neon's does not. Avoids a rebuild the week of the defence. |
| DEC-012 | Notifications: FCM (push) + Resend (email), queued through Redis | Queue decouples request latency from delivery. |
| DEC-013 | Render keep-warm enabled only near the demo window | Continuous pinging to defeat a free tier's idle policy is against the spirit of the plan. Enable for the defence, disable after. |
| DEC-014 | **Time policy: store UTC, display `Africa/Lagos`** | Borrowing, due dates, overdue detection, reminders, audit entries and token expiry are all time. A single canonical zone means they cannot disagree. `TIMESTAMPTZ` everywhere; `DATE` only for date-only values; RFC 3339 on the wire; the server is authoritative for all event timestamps. Full policy: docs/design.md §4A (DES-010). |
| DEC-015 | **Named timezone, not a fixed offset** | `Africa/Lagos` rather than `UTC+1`. A named zone stays correct if the offset ever changes; a hardcoded number does not. |
| DEC-016 | **Embed tzdata in the binary** (`import _ "time/tzdata"`) | The production image is `FROM scratch` and has no `/usr/share/zoneinfo`, so named-zone lookup would fail in production and nowhere else. Costs 0.5 MB. |
