#!/usr/bin/env python3
import argparse
import json
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Dict, List, Tuple


def _json_request(method: str, url: str, api_key: str, body: Dict | None, timeout: int) -> Tuple[int, Dict]:
    payload = None
    headers = {
        "Accept": "application/json",
    }
    if api_key:
        headers["X-Api-Key"] = api_key
    if body is not None:
        payload = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url=url, data=payload, headers=headers, method=method.upper())
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
            return int(resp.status), json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8", errors="replace")
        obj = {}
        if raw:
            try:
                obj = json.loads(raw)
            except Exception:
                obj = {"ok": False, "error": raw}
        return int(e.code), obj


def _derive_status(account: Dict) -> str:
    tags = account.get("tags") if isinstance(account.get("tags"), list) else []
    low_tags = [str(x).strip().lower() for x in tags if str(x).strip()]
    known_status = [
        "registered_pending_login",
        "login_in_progress",
        "ready",
        "login_failed",
        "abandoned",
        "team_owner",
        "team_subscribed",
        "team",
        "normal",
    ]
    for st in known_status:
        if st in low_tags:
            return st
    account_type = str(account.get("account_type", "")).strip().lower()
    if account_type == "team":
        return "team"
    return "normal"


def _select_candidates(accounts: List[Dict], statuses: set[str], include_team: bool) -> List[Dict]:
    picked: List[Dict] = []
    for item in accounts:
        if not isinstance(item, dict):
            continue
        email = str(item.get("email", "")).strip().lower()
        if not email:
            continue
        if bool(item.get("token_alive", False)):
            continue
        account_type = str(item.get("account_type", "")).strip().lower()
        if not include_team and account_type == "team":
            continue
        st = _derive_status(item)
        if statuses and st not in statuses:
            continue
        picked.append(item)
    return picked


def _token_check(api_base: str, api_key: str, email: str, timeout: int) -> Tuple[bool, Dict]:
    quoted = urllib.parse.quote(email, safe="")
    url = f"{api_base}/v1/accounts/{quoted}/token-check"
    code, data = _json_request("POST", url, api_key, {}, timeout)
    ok = (200 <= code < 300) and bool(data.get("ok"))
    return ok, data


def run_once(args: argparse.Namespace) -> int:
    api_base = args.api_base.rstrip("/")
    statuses = {x.strip().lower() for x in args.statuses.split(",") if x.strip()}
    timeout = max(5, int(args.timeout))
    list_url = f"{api_base}/v1/accounts?limit={max(1, int(args.limit))}"

    if args.email:
        target = args.email.strip().lower()
        if args.dry_run:
            print(json.dumps({"ok": True, "mode": "single", "dry_run": True, "email": target}, ensure_ascii=False))
            return 0
        ok, data = _token_check(api_base, args.api_key, target, timeout)
        out = {"ok": ok, "mode": "single", "email": target, "result": data}
        print(json.dumps(out, ensure_ascii=False))
        return 0 if ok else 2

    code, payload = _json_request("GET", list_url, args.api_key, None, timeout)
    if code < 200 or code >= 300:
        print(json.dumps({"ok": False, "step": "list_accounts", "status": code, "error": payload}, ensure_ascii=False))
        return 1

    accounts = payload.get("accounts") if isinstance(payload.get("accounts"), list) else []
    candidates = _select_candidates(accounts, statuses, args.include_team)
    if args.max_items > 0:
        candidates = candidates[: args.max_items]

    results = []
    failed = 0
    for item in candidates:
        email = str(item.get("email", "")).strip().lower()
        if not email:
            continue
        status = _derive_status(item)
        if args.dry_run:
            results.append({"email": email, "status": status, "dry_run": True})
            continue
        ok, data = _token_check(api_base, args.api_key, email, timeout)
        if not ok:
            failed += 1
        results.append({"email": email, "status": status, "ok": ok, "response": data})
        if args.max_failures > 0 and failed >= args.max_failures:
            break

    out = {
        "ok": failed == 0,
        "mode": "batch",
        "total_listed": len(accounts),
        "selected": len(candidates),
        "processed": len(results),
        "failed": failed,
        "dry_run": bool(args.dry_run),
        "results": results,
    }
    print(json.dumps(out, ensure_ascii=False))
    return 0 if failed == 0 else 2


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="Manual/cron login worker using go-team-api token-check")
    p.add_argument("--api-base", default="http://127.0.0.1:18081")
    p.add_argument("--api-key", default="")
    p.add_argument("--limit", type=int, default=300, help="max accounts fetched from /v1/accounts")
    p.add_argument("--max-items", type=int, default=30, help="max selected candidates for token-check")
    p.add_argument(
        "--statuses",
        default="registered_pending_login,login_failed,normal",
        help="comma-separated statuses to process",
    )
    p.add_argument("--include-team", action="store_true", help="allow processing team accounts")
    p.add_argument("--max-failures", type=int, default=10, help="stop batch after N failures")
    p.add_argument("--timeout", type=int, default=90)
    p.add_argument("--email", default="", help="process single email only")
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--loop", action="store_true", help="keep running periodically")
    p.add_argument("--interval-seconds", type=int, default=120)
    return p


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()

    if not args.loop:
        return run_once(args)

    interval = max(10, int(args.interval_seconds))
    while True:
        code = run_once(args)
        if code not in (0, 2):
            return code
        time.sleep(interval)


if __name__ == "__main__":
    raise SystemExit(main())
