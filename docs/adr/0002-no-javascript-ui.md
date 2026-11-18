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

## Deviation: the operations list updates itself in place

`/operations` defaults to a rolling window (`last=15m`) and keeps it current
while the page is open. The first attempt used a document-level refresh
(`<meta http-equiv="refresh">`), which reloads the whole page every minute: it
loses the scroll position, flashes, and interrupts anyone typing in the filter
form. That was rejected as "a hacky browser reload" by the operator.

What runs now is a single inline script with one job: fetch the same URL, parse
the response, and replace the contents of the `#live` section. Consequences of
keeping the swap narrow:

- the page is still server-rendered — the script never builds markup, it moves
  the server's own markup into place;
- scroll position, the filter form, and everything outside `#live` are untouched;
- the swap is a 140 ms opacity fade (skipped under `prefers-reduced-motion`)
  rather than a jump cut;
- nothing polls while the tab is hidden;
- a failed fetch leaves the previous content on screen and retries next tick;
- with JavaScript disabled the page still works exactly as before, and gets the
  document refresh inside `<noscript>` as the fallback.

This is the narrowest deviation we could find that meets the requirement. Any
future client-side behaviour should be held to the same rule: swapping what the
server already rendered, never constructing a view in the browser.
