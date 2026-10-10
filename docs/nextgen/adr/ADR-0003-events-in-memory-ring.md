# ADR-0003 — Events are an in-memory bounded ring with push subscribers

- Status: ACCEPTED (2026-10-10)
- Requirements: FR-EVT-01..03, NFR-01
- Tickets: P1-02

## Context

`docker compose up`, `docker events` and `docker run --rm` without a wait
condition, and future UIs, need container, image, network and volume events.
Polling per container costs wakeups (NFR-01).

## Decision

The engine publishes every state transition to an event bus:

- a ring of the last 1024 events, for `since`/`until` replays;
- subscribers receive events through a buffered channel (256). A subscriber
  that falls behind is disconnected rather than allowed to grow memory or to
  block the engine. Its HTTP stream ends and the client may reconnect with
  `since`;
- nothing is written to disk. dockerd does not persist events either, and
  durable state is already the container record.

Publishing an event is a non-blocking channel send under one mutex. No
goroutine runs while there are no subscribers.

## Consequences

- Events are lost across a daemon restart, as with dockerd.
- Memory is bounded (1024 events × ~300 bytes ≈ 300 KiB at worst).
