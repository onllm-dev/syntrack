# onWatch

Go CLI for AI quota tracking. Polls 6 providers → SQLite → Material Design 3 dashboard.

## Task

Background daemon (<50MB RAM) tracking: Anthropic, Synthetic, Z.ai, Copilot, Codex, Antigravity.

## Code Map

```
main.go                     # CLI entry, daemon lifecycle
internal/
├── api/                    # HTTP clients + types per provider
│   └── {provider}_client.go, {provider}_types.go
├── store/                  # SQLite persistence per provider
│   └── store.go (schema), {provider}_store.go
├── tracker/                # Poll orchestration per provider
├── agent/                  # Background polling agents
├── web/                    # Dashboard server
│   ├── handlers.go         # API endpoints
│   ├── static/             # Embedded JS/CSS (embed.FS)
│   └── templates/          # HTML templates
├── config/                 # Config + container detection
└── notify/                 # Email + push notifications
```

## Objectives

1. **TDD-first**: Test → fail → implement → pass
2. **RAM-bounded**: 40MB limit, single SQLite conn, lean HTTP
3. **Single binary**: All assets via `embed.FS`

## Operations

```bash
./app.sh --build            # Build before running
./app.sh --test             # go test -race -cover ./...
go test -race ./... && go vet ./...   # Pre-commit (mandatory)
```

## Guardrails

| Rule | Reason |
|------|--------|
| Never commit `.env`, `.db`, binaries | Security |
| Never log API keys | Security |
| Parameterized SQL only | Injection prevention |
| `context.Context` always | Leak prevention |
| `-race` before commit | Data race detection |
| `subtle.ConstantTimeCompare` for creds | Timing attacks |
| Bounded queries (cycles≤200, insights≤50) | Memory caps |

## Notes

**Adding a provider:**
1. `internal/api/{provider}_client.go` + `_types.go`
2. `internal/store/{provider}_store.go`
3. `internal/tracker/{provider}_tracker.go`
4. `internal/agent/{provider}_agent.go`
5. Add to `internal/web/handlers.go` endpoints
6. Update dashboard JS in `internal/web/static/app.js`

**API Docs:** See `docs/` for provider-specific setup (COPILOT_SETUP.md, CODEX_SETUP.md, ANTIGRAVITY_SETUP.md)

**Containers:** `IsDockerEnvironment()` in `config.go` detects Docker/K8s. Containers run foreground only.

**Release:** `./app.sh --release` → cross-compile 5 platforms → include all binaries in GitHub release.
