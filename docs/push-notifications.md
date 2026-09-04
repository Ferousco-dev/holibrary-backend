# Push notifications

Push runs through Firebase Cloud Messaging. Firebase is in this system for push
and nothing else (DEC-017): Crashlytics and Performance Monitoring are
client-side products with no Go server SDK and cannot observe this API.

## How it is wired

```
service                 outbox                worker                FCM
  |                       |                     |                     |
  | queue a notification  |                     |                     |
  |---------------------->|                     |                     |
  |                       |   pending messages  |                     |
  |                       |<--------------------|                     |
  |                       |                     | re-check the state  |
  |                       |                     | still true?         |
  |                       |                     |-------------------->|
  |                       |   mark sent         |                     |
  |                       |<--------------------|                     |
```

The worker re-reads the database before sending, so a reminder queued last
night for a book returned this morning is marked superseded and never sent.

## Configuration

One environment variable:

```
FIREBASE_SERVICE_ACCOUNT
```

A Firebase service account key, as raw JSON or base64. Base64 is preferred: a
multi-line JSON blob survives some dashboards badly.

The **project id is read out of the key**, not from a second variable. Two
sources for one fact is two things that can disagree, and that disagreement
fails at send time with an error that does not explain itself.

### Obtaining a key

1. Firebase console, project settings, Service accounts
2. Generate new private key; a JSON file downloads
3. Encode and set it:

   ```bash
   base64 -i ~/Downloads/<project>-firebase-adminsdk-*.json | tr -d '\n' | pbcopy
   ```

4. Paste as `FIREBASE_SERVICE_ACCOUNT` in the Render dashboard, and redeploy.

The key is a live credential. It belongs in the deployment environment and
nowhere else: not in the repository, not in a chat message, and not left in a
Downloads folder that syncs to a cloud drive. Delete the local copy once it is
set. If it leaks, revoke it in the Firebase console and generate another; the
service account is scoped to `firebase.messaging` alone, so a leaked key can
send notifications but cannot read the project's data or change its settings.

### Confirming it works

On boot the log says which channels have a sender:

```
INFO push notifications enabled firebase_project=holibrary
INFO notification worker started channels=["push","email"]
```

If `channels` lists only `email`, push has no sender and every push message
will stay pending. The line above it names the reason.

## What is still missing

The server can send. Nothing can receive yet.

A browser has to ask permission, register a service worker, obtain a token and
post it to `POST /me/devices`. Until a device does that, `device_tokens` is
empty and no push has anywhere to go.

The front end needs, in order:

1. A **Web Push certificate** from the Firebase console (Project settings,
   Cloud Messaging, Web configuration). This is the VAPID public key and is
   safe to ship in client code.
2. A **service worker** at the site root, registered on the page.
3. A permission prompt, asked at a moment the reader understands, not on first
   load.
4. The resulting token posted to `POST /me/devices`.

One limitation worth stating plainly at the defence: **iOS Safari delivers web
push only when the site has been added to the home screen.** On iPhone, a
member who simply visits the site in a browser will not receive push at all.
Email remains the channel that reaches everybody, which is why due-date
reminders and reserve alerts are queued for email regardless.
