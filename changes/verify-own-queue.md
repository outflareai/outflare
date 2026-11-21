---
kind: fix
area: general
---
Background work no longer waits behind a bulk email verification. Verification now runs on its own few workers, and addresses a verifier could not answer are asked again after five minutes instead of at once, so a slow or busy verifier can no longer hold up syncs, sends and alerts for half an hour at a time.
