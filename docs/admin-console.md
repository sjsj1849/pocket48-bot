# Pocket48 Console

The console is a private operations UI for service health, curated configuration,
logs, and interactive browser authentication.

## Build

```bash
cd admin-ui
npm ci
npm run build
cd ..
go build -o pocket48-admin ./cmd/admin
```

The Vite build is embedded from `internal/admin/web` into the Go binary.

## Runtime

Create a password with at least 16 characters at
`storage/admin-password`, copy `deploy/pocket48-admin.service` to systemd, and
proxy `/pocket48/` to `http://127.0.0.1:8787/`. The reverse proxy must forward
WebSocket upgrades for the interactive noVNC browser session.

The service only binds to localhost. Authentication uses an HttpOnly,
SameSite=Strict session cookie and CSRF protection for state-changing requests.
