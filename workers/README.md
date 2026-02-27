# workers

External worker scripts.

## js_oauth_worker

Protocol OAuth worker (JavaScript, no Python):

- Path: `workers/js_oauth_worker/worker.mjs`
- Uses Supabase as source of truth (`accounts_pool`).
- Performs login + oauth token exchange + token persistence.
- Can run on Node or Deno.

See: `workers/js_oauth_worker/README.md`.

## manual_login_worker.py

Purpose:
- Trigger login/token-check for accounts that are registered but still not alive.
- Can run manually once, in loop mode, or via scheduler.

### Single account

```bash
python workers/manual_login_worker.py \
  --api-base http://127.0.0.1:18081 \
  --api-key opkey \
  --email foo@example.com
```

### Batch once

```bash
python workers/manual_login_worker.py \
  --api-base http://127.0.0.1:18081 \
  --api-key opkey \
  --statuses registered_pending_login,login_failed,normal \
  --max-items 50
```

### Loop mode (periodic)

```bash
python workers/manual_login_worker.py \
  --api-base http://127.0.0.1:18081 \
  --api-key opkey \
  --loop \
  --interval-seconds 180
```

Notes:
- Requires `go-team-api` running and reachable.
- If `API_KEYS` is configured in `go-team-api`, pass `--api-key`.
- The worker calls `POST /v1/accounts/{email}/token-check` and writes results via API.
