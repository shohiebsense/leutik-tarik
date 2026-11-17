# Pilot Projects

Absolute paths on this machine (`/Users/user/Projects/...`).

## Background — why this document and why `leutik-tarik`

The trigger is the **fare-membership testing sessions held yesterday and today**.
Every session surfaced issues or findings from a QA or Product point of view. When that
happens, tracing *what actually happened* currently means one of two paths:

1. Manually trace it across logs, DB rows, spans, and deploy state — slow, and often
   there is no single place that connects a failing request to its own events.
2. Ask the agent, hand it a few clues, and have it read the pattern and the data to
   conclude what happened. That works, but it **burns tokens and creates a dependency
   on the agent** just to answer "what happened to this request?"

The point of `leutik-tarik` is to remove that dependency: make it self-serve so a QA,
Product, or on-call person can open the platform, pick the failing/slow operation, and
follow the evidence (request → dependency → error → log snippet → profile) themselves,
without asking the agent. The pilot projects above are the first systems that need this
traceability; fare membership is the one currently causing the most tracing load.

## 1. Fare Membership (and related services)

| Name | Absolute path | Notes |
|------|---------------|-------|
| Fare Membership — repo root | `/Users/user/Projects/fare-membership` | Monorepo working root (docs, plans, compose) |
| Fare Membership — Go backend | `/Users/user/Projects/fare-membership/fare-membership` | Go + Gin core API: customer, profile, media, orders, ABT journeys, payments |
| Fare Membership — Back Office | `/Users/user/Projects/fare-membership/fare-membership-back-office` | SvelteKit admin UI |
| KYC OCR service | `/Users/user/Projects/fare-membership/kyc-ocr-service` | Related service in the monorepo |
| Partition manager | `/Users/user/Projects/fare-membership/partition-manager` | Related service in the monorepo |
| DB host forwarder | `/Users/user/Projects/fare-membership/db-host-forwarder` | K8s manifests, forwards DB host |
| Fare Membership secrets (dev) | `/Users/user/Projects/fare-membership-service-secret/dev` | |
| KYC OCR secrets (dev) | `/Users/user/Projects/fare-membership-service-ocr-secret/dev` | |
| Fare Membership docs | `/Users/user/Projects/fare-membership-docs` | |
| Jaklingko web back office | `/Users/user/Projects/javascript/jaklingko-web-backoffice` | Separate auth-side back office (repo `aino-sapps`) |
| Jaklingko web back office secrets | `/Users/user/Projects/secrets/jaklingko-web-backoffice-secret` | |

Related upstream (auth, source of record for passwords): `jaklingko-service-auth` — not cloned here.

## 2. Astrompanion

| Name | Absolute path | Notes |
|------|---------------|-------|
| Astrompanion | `/Users/user/Projects/experimental/astrompanion` | Go backend + Svelte 5 frontend, PostgreSQL + MongoDB; GitHub `shohiebsense/astrompanion`, branch `feat-astrompanion-battle` |
| Astrompanion backend | `/Users/user/Projects/experimental/astrompanion/backend` | |
| Astrompanion MVP (older) | `/Users/user/Projects/experimental/astrompanion-mvp` | Superseded |

## 3. Fare Distribution

| Name | Absolute path | Notes |
|------|---------------|-------|
| Fare Distribution service | `/Users/user/Projects/go/fare-distribution` | GitLab `jpai-development/fare-distribution-service`, branch `development`; serves SQL updates to L1 readers |
| Fare Distribution Back Office | `/Users/user/Projects/fare-distribution-backoffice` | SvelteKit; GitLab `jpai-development/fare-distribution-backoffice` |
| Fare Distribution secrets (prod) | `/Users/user/Projects/fare-distribution-service-secret/prod` | |
| Fare Engine | `/Users/user/Projects/javascript/fare-engine` | Upstream fare calculation service used by the above |
| Logman fare engine secrets | `/Users/user/Projects/javascript/logman-fare-engine-secret` | |
| Legacy JS fare-distribution reference | `/Users/user/Projects/javascript/faredist_21-01-2026` | Reference only, not production |

## Notes

- GitLab default branch for all `jpai-development` services is `development`; `master` is production and only pushed on explicit confirmation.
- Service names/paths are per-machine clones; the same repos live on `repo.jatelindo.co.id` under `jpai-development/` (Astrompanion is on GitHub instead).
