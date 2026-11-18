# ADR 0002 — Zero-JavaScript UI

Status: accepted (build prompt §4, §13.4).

## Decision

The browser executes zero JavaScript. All views are server-rendered via
`html/template`; forms/links/query params drive navigation; charts, waterfalls,
service maps, and flamegraphs are **server-generated SVG** with an accessible
HTML table fallback for every visualization.

## Rationale

Lower operational complexity, no Node toolchain, works with JS disabled, and
each page is directly deployable as one Go service.

## Consequences

- No SPA/hydration/client-side charting. Interactions are GET/POST → new page.
- Where a visual interaction cannot be delivered without JS, implement the
  clearest server-rendered alternative and document the trade-off.

## Deviation: the operations list refreshes itself

`/operations` defaults to the last 15 minutes and re-requests that window every
60 seconds. Without JavaScript the only way to advance a window is to re-ask the
server, so this uses a document-level refresh (`<meta http-equiv="refresh">`).
The trade-off is a full reload: the scroll position resets and the page repaints
every minute. Two guards keep it from fighting the reader: a window pinned with
`from`/`to` never refreshes, and a live page offers a "freeze this window" link
that pins exactly what is on screen. An in-place update would need a script, and
that is the thing this ADR exists to avoid.
