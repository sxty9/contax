# holistic-service-template

A starting point for a **holistic service**: a Go backend behind the holistic Caddy proxy
plus a dashboard plugin built on the **`@holistic/ui`** SDK (consumed, never vendored). The
example ships a working **rights interface** that `privleg` can configure per user.

```
Browser ── https://holistic.local (Caddy, same-origin) ─┐
  ├─ /                          → holistic SPA (bundles this plugin)
  ├─ /api/*                     → holistic backend       (127.0.0.1:8770)
  └─ /api/services/contax/*  → contaxd (Go)        (127.0.0.1:8777)
```

- **Single sign-on:** the daemon validates the same holistic session (HS256 JWT in the
  `h_access` cookie, secret `/etc/holistic/jwt-secret`) — no separate login.
- **Roles = Linux (single source of truth):** admin = membership in the `sudo` group.
- **Least privilege:** the daemon runs as an unprivileged system user and is sandboxed by
  systemd. It performs no privilege escalation.

## Prerequisites

The [holistic](https://github.com/sxty9/holistic) repo must be present **as a sibling**
(`../holistic`) with the dashboard installed — it provides the `@holistic/ui` SDK and the
SPA that bundles this plugin.

```
git clone git@github.com:sxty9/holistic.git
git clone git@github.com:sxty9/holistic-service-template.git contax
```

## Quickstart

```bash
cd contax
./service init mytool        # rename the placeholder 'contax' → 'mytool' everywhere
sudo ./service setup         # build, wire systemd + Caddy, declare rights, rebuild the SPA
```

After `setup`, the service appears in the holistic sidebar. Other commands: `service build`,
`service start|stop|restart`, `service status`, `service update`, `service uninstall [--purge]`.

## API surface

contax serves `/api/services/contax/` behind the shared holistic session (validated from the
`h_access` cookie — no separate login). Every signed-in user manages their OWN contacts and groups,
so the routes need only a valid session; mutations add the CSRF double-submit guard. contax declares
**no** fine-grained rights (`permissions/contax.json` is empty): who a user may SEE internally is
governed centrally by privleg's `hc_*` contact groups, not by a contax permission.

| Method | Path | Purpose |
|---|---|---|
| GET | `contacts` | the full list — internal (computed from shared `hc_*` groups) + external |
| GET | `lookup?q=&includeGroups=1` | typeahead for the shared `ContactPicker`; groups when asked |
| POST · PUT · DELETE | `contacts` · `contacts/{id}` | external-contact CRUD (CSRF on writes) |
| POST | `contacts/{id}/hide` · `internal/{username}/hide` | per-user visibility (CSRF) |
| GET · POST · PUT · DELETE | `groups` · `groups/{id}` | personal contact groups (CSRF on writes) |
| POST · DELETE | `groups/{id}/members` · `.../members/{ref}` | curate a group's members (CSRF) |
| GET | `groups/{id}/members` | resolve a group to its member contacts (ContactPicker expand) |
| GET | `internal/groups/{id}/members` | **machine-to-machine**: a group's internal usernames |

Personal contact **groups** are the entity contax owns for the shared `@holistic/ui` `ContactPicker`
and for sibling services (e.g. hosuto server grants) that reference a group by id. The last route
carries no session — it is authenticated solely by a shared secret in the `X-Contax-Internal-Secret`
header (constant-time compare), so a caller can keep a "shared with this group" membership live.
`setup` provisions that secret at `/etc/holistic/contax-internal-secret` (group-readable); an
unprovisioned host serves 503 there (fail closed). Backend routing + enforcement live in
`backend/internal/api/api.go`.

## Local development

```bash
# Backend
cd backend && go build ./... && go vet ./...

# UI plugin in the holistic dashboard (holistic as a sibling repo)
ln -sfn "$PWD/ui" ../holistic/frontend/external/contax
( cd ../holistic/frontend && pnpm --filter @holistic/app dev )   # http://localhost:5173
```

UI imports are restricted to `@holistic/ui` + `react` (enforced by holistic's
`eslint.services.cjs` at SPA build time).

## Layout

```
service                     single-file CLI: init / setup / build / lifecycle
permissions/contax.json  rights manifest (drop-in for privleg)
backend/                    Go daemon (contaxd)
  cmd/contaxd/             entry point — listens on 127.0.0.1:8777
  internal/auth/              shared-JWT validation + live group/admin resolution + CSRF
  internal/api/               HTTP routes under /api/services/contax/
  internal/contacts/          the read-model: internal (live) + external + groups, one access point
  internal/store/             the only owned state: external contacts, hidden sets, personal groups
  internal/directory,profile,instance,gravatar/  live readers of the shared sources (groups, profile, mail domain, avatar)
ui/                         @holistic/ui plugin (linked into holistic/frontend/external/<id>)
```

### Going further: privileged actions

This template escalates nothing. If your service must perform OS-level writes, follow the
holistic pattern: a narrow `/usr/local/sbin` wrapper allow-listed in `sudoers.d`, invoked via
`sudo -n`, with `NoNewPrivileges=false` in the unit (see `sxty9/hostek` for a worked example).

## License

MIT — see [LICENSE](LICENSE).
