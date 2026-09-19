#!/usr/bin/env python3
"""Compare fresh Sub2 source probes with the running Cockpit ticket harvester.

Requires the temporary Sub2 diagnostic binary. Credentials remain in memory;
the report only contains HTTP status, ticket length, protocol, and timestamps.
"""
import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import select
import subprocess
import time
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("--account-id", required=True)
parser.add_argument("--node-label", required=True)
parser.add_argument("--sub2-probe", type=Path, required=True)
parser.add_argument("--version", default="0.155.1")
parser.add_argument("--output", type=Path, required=True)
args = parser.parse_args()
base_dir = Path.home() / ".antigravity_cockpit_dev"
config = json.loads((base_dir / "codex_local_access.json").read_text())
if config["accountIds"] != [args.account_id]:
    raise SystemExit("The Cockpit pool must contain only the requested test account")
base_url = f"http://127.0.0.1:{config['port']}/v1/cockpit/tickets"
auth_file = base_dir / "codex_local_access_sidecar" / "auths" / (args.account_id + ".json")
headers = {"Authorization": "Bearer " + config["apiKey"]}


def observe_exit():
    """Observe the same host's CDN trace; keep only a hash of its exit IP."""
    try:
        completed = subprocess.run(
            ["curl", "--silent", "--show-error", "--max-time", "10",
             "--proxy", config["codexTicket"]["proxyUrl"],
             "https://chatgpt.com/cdn-cgi/trace"],
            capture_output=True, text=True, timeout=12, check=True)
        fields = dict(line.split("=", 1) for line in completed.stdout.splitlines()
                      if "=" in line and "<" not in line)
        if "ip" in fields:
            return {"fingerprint": hashlib.sha256(fields["ip"].encode()).hexdigest()[:12],
                    "country": fields.get("loc"), "colo": fields.get("colo")}
    except (subprocess.SubprocessError, OSError):
        pass
    return {"unavailable": True}


def status(refresh=False):
    req = urllib.request.Request(base_url + ("/refresh" if refresh else ""),
                                 data=b"" if refresh else None, headers=headers)
    with urllib.request.urlopen(req, timeout=5) as response:
        return json.load(response)


def cockpit_probe():
    started = int(time.time() * 1000)
    status(True)
    deadline = time.monotonic() + 35
    while time.monotonic() < deadline:
        snapshot = status()
        tickets = snapshot["tickets"]
        if tickets and all(t["lastAttemptAt"] >= started and not t["refreshing"] for t in tickets):
            return [{"implementation": "cockpit", "model": t["model"],
                     "http": t["lastHttpStatus"], "length": t["lastLength"],
                     "startedAt": t["lastAttemptAt"], "error": t["lastError"],
                     "fresh292": t["lastLength"] == 292 and t["capturedAt"] >= started,
                     "cachedTicketStillReady": t["ready"]} for t in tickets]
        time.sleep(0.25)
    raise TimeoutError("Cockpit did not finish a fresh probe within 35 seconds")


def sub2_probe():
    stdout, _ = sub2_process.communicate(input="\n", timeout=35)
    if sub2_process.returncode:
        raise RuntimeError("Sub2 diagnostic exited unsuccessfully")
    rows = []
    for line in stdout.splitlines():
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        if row.get("implementation") == "sub2_original":
            row["fresh292"] = row["http"] == 200 and row["length"] == 292
            rows.append(row)
    if len(rows) != len(config["codexTicket"]["models"]):
        raise ValueError("Sub2 diagnostic did not return both model results")
    return rows


# Avoid attributing an already-running request to this node comparison.
deadline = time.monotonic() + 30
while any(t["refreshing"] for t in status()["tickets"]):
    if time.monotonic() > deadline:
        raise TimeoutError("A previous Cockpit probe is still running")
    time.sleep(0.25)

exit_before = observe_exit()
sub2_process = subprocess.Popen(
    [str(args.sub2_probe), "--auth-file", str(auth_file),
     "--proxy", config["codexTicket"]["proxyUrl"], "--version", args.version,
     "--wait-for-start"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
    stderr=subprocess.PIPE, text=True)
try:
    # Complete Go package initialization before sending either probe.
    deadline = time.monotonic() + 15
    ready, pending = False, b""
    while not ready:
        remaining = deadline - time.monotonic()
        if remaining <= 0 or not select.select([sub2_process.stdout], [], [], remaining)[0]:
            raise TimeoutError("Sub2 diagnostic did not become ready")
        chunk = os.read(sub2_process.stdout.fileno(), 4096)
        if not chunk:
            raise RuntimeError("Sub2 diagnostic exited before readiness")
        pending += chunk
        while b"\n" in pending:
            line, pending = pending.split(b"\n", 1)
            try:
                ready = json.loads(line).get("ready") is True
            except (json.JSONDecodeError, AttributeError):
                continue
            if ready:
                break
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        sub2_future = pool.submit(sub2_probe)
        cockpit_future = pool.submit(cockpit_probe)
        results = sub2_future.result() + cockpit_future.result()
finally:
    if sub2_process.poll() is None:
        sub2_process.kill()
        sub2_process.communicate()
report = {"node": args.node_label, "recordedAt": int(time.time() * 1000),
          "clientVersion": args.version,
          "exitBefore": exit_before, "exitAfter": observe_exit(),
          "startSkewMs": max(r["startedAt"] for r in results) - min(r["startedAt"] for r in results),
          "results": results}
args.output.parent.mkdir(parents=True, exist_ok=True)
with args.output.open("a") as output:
    output.write(json.dumps(report, ensure_ascii=False) + "\n")
print(json.dumps(report, ensure_ascii=False, indent=2))
