# js_oauth_worker

Protocol OAuth login worker in JavaScript.

## What it does

- Reads candidate accounts from Supabase `accounts_pool`.
- Runs protocol login (`email + password + OTP`) and exchanges OAuth token.
- Persists `access_token / refresh_token / id_token` back to `accounts_pool`.
- Optional CPA auth-file upload.

No Python dependency.

## Runtime

- Node.js >= 20 (recommended)
- Deno

## Config

Copy `.env.example` and fill secrets:

- `SUPABASE_URL`
- `SUPABASE_SERVICE_KEY`
- `MAIL_WORKER_DOMAIN`
- `MAIL_ADMIN_PASSWORD`

## Run (Node)

Single run:

```bash
node workers/js_oauth_worker/worker.mjs
```

Dry run:

```bash
node workers/js_oauth_worker/worker.mjs --dry-run
```

Single account:

```bash
node workers/js_oauth_worker/worker.mjs --email foo@bar.com
```

Loop mode:

```bash
node workers/js_oauth_worker/worker.mjs --loop --interval-seconds 180
```

## Run (Deno)

```bash
deno run --allow-net --allow-env workers/js_oauth_worker/worker.mjs --loop
```

## Output

The worker prints JSON summary to stdout. This is log-platform friendly and easy to parse by schedulers.
