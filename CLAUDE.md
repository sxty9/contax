# CLAUDE.md

Template for a **holistic service**. A developer clones it, runs `./service init <name>`, and
builds out the backend + dashboard plugin. The holistic SDK (`@holisdk/ui`) is **consumed
only** — never vendored or modified here.

## Where things are

- `service` — the CLI. Auto-detects the service id from `permissions/<id>.json`; owns
  `init`/`setup`/lifecycle and generates the systemd unit, Caddy route and rights drop-in inline.
- `backend/internal/auth/auth.go` — shared-JWT (`h_access`) validation, live OS group/admin
  resolution, CSRF. Service-agnostic; reuse as-is.
- `backend/internal/api/api.go` — the HTTP surface under `/api/services/<id>/`. The `guard`
  helper does auth → optional right → optional CSRF. Add routes here.
- `backend/internal/rights/` — the `hp_*` group constant(s); mirror `permissions/<id>.json`.
- `ui/index.tsx` — default-exports the `ServicePlugin`; `id` MUST equal the manifest `service`.
- `ui/Dashboard.tsx` — the plugin UI; renders **only** `@holisdk/ui`, gates with `userHasRight`.

## Rules

- Enforce every right as `isAdmin || group ∈ user.groups`, in both the backend and the UI.
- Keep three things in sync: `permissions/<id>.json` ⇄ `internal/rights` ⇄ the UI right constant.
- UI may import only `@holisdk/ui` and `react` (holistic's `eslint.services.cjs` enforces it).
- The daemon runs unprivileged and escalates nothing. Privileged work needs a narrow sudo
  wrapper (see `<org>/hostek`), not blanket sudo.

## Verify (from the repo root)

```bash
(cd backend && go build ./... && go vet ./...)
python3 ../holistic/services/dashboard/lib/holistic-perms.py validate ./permissions
```

## Holistic constitution

This service is designed, implemented and reviewed against the binding **Holistic axioms and
implementation rules** — the shared constitution that governs every Holistic service (single
source of truth, reuse-before-build, uniform structure, atomic access, passive data pools,
minimalism, and the rest).

That constitution is **not vendored into this repo**. It is maintained centrally in the
Holistic axiom store and delivered to each implementation through the prompt that commissions
the work, so the current wording always applies — independent of this file's age. Only a
*reference* to the shared constitution belongs here; the store's concrete location lives solely
in the runtime configuration, never in this file.

When you change this repo, validate every change against the constitution as delivered to you,
and do not copy its text back into the repository.

<!-- holistic:constitution:begin -->
# Holistic — Verfassung

Für dieses Repository gelten die Holistic-Axiome und Implementierungsregeln.
Ihr verbindlicher Wortlaut wird nicht hier geführt, sondern mit jedem
Implementierungsauftrag mitgeliefert. So gilt immer der aktuelle Stand.

**Arbeitest du im Auftrag von Mercury:** Der Wortlaut steht vollständig in
deinem Prompt. Er hat Vorrang vor jeder anderen Fassung, die dir begegnet.

**Arbeitest du in einer von Hand geöffneten Sitzung:** Implementiere nicht
selbst. Lege die Arbeit als ToDo in Mercury an und führe es aus — dann kommt
der verbindliche Wortlaut auf dem regulären Weg. Der Verfassungs-Bestand wird
in der Laufzeit-Konfiguration der Instanz benannt.
<!-- holistic:constitution:end -->
