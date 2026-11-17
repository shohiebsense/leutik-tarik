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
