# Security Policy

## Reporting a vulnerability

Please report security issues privately to **security@partyline.sh** — do not open a
public issue for anything exploitable. We aim to acknowledge within 72 hours.

## Security model (summary)

- **End-to-end encrypted sessions.** Joiner↔host traffic runs over a Noise **NNpsk0**
  channel (X25519 / ChaCha20-Poly1305 / BLAKE2s) via `flynn/noise`. The 32-byte link key
  is the pre-shared key and travels in the share link, never in this repo.
- **Blind relay.** The relay (`relay/`) splices ciphertext between peers and holds no key —
  it cannot read or modify a session.
- **Signed join identity.** Optional join assertions are Ed25519-signed by the control
  plane; the host verifies them with a baked-in **public** key and fails closed.
- **Driving wall.** Input reaches the shared PTY only for the host or a verified
  full-access user — default-deny.

No secrets are committed to this repository; all runtime configuration is read from the
environment.

## Supported versions

Security fixes target the latest release.
