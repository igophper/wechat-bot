# Changelog

## 0.1.0 — 2026-09-23

First public release of the independent Go client for the WeChat iLink bot protocol. Requires Go 1.26.0 or later; APIs may change during v0.

### Features

- QR login with terminal rendering, login redirects, verification-code callbacks, and local credential storage.
- Text and Markdown replies, encrypted image/file/video uploads, incoming media metadata, and image download/decryption.
- Long-poll message reception with configurable retry backoff and session recovery.
- Error-returning message handlers, bounded concurrency with per-user ordering, and cancellation-aware shutdown.
- Memory and file cursor stores. A batch is acknowledged only after its handlers succeed; applications can provide their own storage.
- Bounded context-token caching with explicit restore and removal methods for application-managed persistence.
- Configurable timeouts and response/media size limits, plus host policies for server-supplied URLs and redirects.
- A runnable login/reply example with optional timing logs and durable recording of permanently failed messages.
- Chinese and English documentation, package documentation, and contribution/security guidance.

### Validation and limitations

- A maintainer verified text reception and replies using an existing live account session. Fresh QR login, verification-code branches, and media transfers have not yet been verified on a live account; automated regression tests use simulated servers.
- Sending depends on platform account eligibility and conversation context. Do not assume that a bot token permits arbitrary outbound messaging.
- Failed or interrupted batches may replay after restart. Applications must make side effects idempotent; exactly-once delivery is not guaranteed.
- Context tokens are held in memory and are not persisted automatically. File cursor storage is intended for one process per account.
- URL checks reject untrusted non-public IP literals but do not resolve hostnames. Explicitly configured base hosts and allowlisted hosts are trusted exceptions; HTTPS base URLs require HTTPS targets.
