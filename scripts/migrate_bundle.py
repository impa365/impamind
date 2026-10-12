#!/usr/bin/env python3
"""Export .impamind from local and import into remote (one-shot migrate)."""
from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request
from http.cookiejar import CookieJar

LOCAL = os.environ.get("IMPAMIND_LOCAL_URL", "http://127.0.0.1:30303")
REMOTE = os.environ.get("IMPAMIND_REMOTE_URL", "https://memoria.impa365.cloud")
LOCAL_PASS = os.environ.get("IMPAMIND_LOCAL_PASSWORD", "impamind-admin-local")
REMOTE_PASS = os.environ.get("IMPAMIND_REMOTE_PASSWORD", "")
BUNDLE_PASS = os.environ.get("IMPAMIND_BUNDLE_PASSPHRASE", "")
OUT = os.environ.get("IMPAMIND_BUNDLE_FILE", os.path.join(os.path.dirname(__file__), "..", "tmp", "migrate.impamind"))


class Client:
    def __init__(self, base: str):
        self.base = base.rstrip("/")
        self.cj = CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cj))

    def login(self, password: str):
        return self._json("POST", "/auth/login", {"password": password})

    UA = (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
    )

    def _headers(self, extra: dict | None = None) -> dict:
        h = {
            "Accept": "application/json",
            "Connection": "close",
            "User-Agent": self.UA,
        }
        if extra:
            h.update(extra)
        return h

    def _json(self, method: str, path: str, body: dict | None = None):
        data = None
        headers = self._headers()
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(self.base + path, data=data, headers=headers, method=method)
        with self.opener.open(req, timeout=120) as resp:
            raw = resp.read()
            return json.loads(raw.decode()) if raw else {}

    def export_bundle(self, passphrase: str, path: str):
        body = json.dumps({
            "passphrase": passphrase,
            "include_global": True,
            "project": "",
        }).encode("utf-8")
        req = urllib.request.Request(
            self.base + "/api/bundle/export",
            data=body,
            headers=self._headers({"Content-Type": "application/json", "Accept": "application/zip"}),
            method="POST",
        )
        with self.opener.open(req, timeout=600) as resp:
            data = resp.read()
        os.makedirs(os.path.dirname(os.path.abspath(path)) or ".", exist_ok=True)
        with open(path, "wb") as f:
            f.write(data)
        return len(data)

    def import_bundle(self, passphrase: str, path: str, overwrite: bool = True):
        boundary = "----impamind" + str(int(time.time()))
        with open(path, "rb") as f:
            blob = f.read()
        parts = []
        def field(name: str, value: str):
            parts.append(
                f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n".encode()
            )
        field("passphrase", passphrase)
        if overwrite:
            field("overwrite_vault", "1")
        parts.append(
            (
                f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"bundle.impamind\"\r\n"
                f"Content-Type: application/zip\r\n\r\n"
            ).encode()
            + blob
            + b"\r\n"
        )
        parts.append(f"--{boundary}--\r\n".encode())
        body = b"".join(parts)
        req = urllib.request.Request(
            self.base + "/api/bundle/import",
            data=body,
            headers=self._headers({
                "Content-Type": f"multipart/form-data; boundary={boundary}",
            }),
            method="POST",
        )
        with self.opener.open(req, timeout=600) as resp:
            raw = resp.read()
            ctype = resp.headers.get("Content-Type", "")
            if "json" not in ctype and not raw.lstrip().startswith(b"{"):
                raise RuntimeError(
                    "import returned non-JSON (status %s, content-type %s) — "
                    "VPS image probably lacks /api/bundle/*; pull impa365/impamind:1 and redeploy"
                    % (getattr(resp, "status", "?"), ctype)
                )
            return json.loads(raw.decode())


def main():
    if len(BUNDLE_PASS) < 8:
        print("set IMPAMIND_BUNDLE_PASSPHRASE (≥8 chars)", file=sys.stderr)
        return 2
    if not REMOTE_PASS:
        print("set IMPAMIND_REMOTE_PASSWORD", file=sys.stderr)
        return 2

    local = Client(LOCAL)
    print("login local", LOCAL)
    local.login(LOCAL_PASS)
    print("export →", OUT)
    n = local.export_bundle(BUNDLE_PASS, OUT)
    print("exported bytes", n)

    remote = Client(REMOTE)
    print("login remote", REMOTE)
    try:
        remote.login(REMOTE_PASS)
    except urllib.error.HTTPError as e:
        print("remote login failed", e.code, e.read().decode(errors="replace")[:500], file=sys.stderr)
        return 1
    print("import…")
    try:
        rep = remote.import_bundle(BUNDLE_PASS, OUT, overwrite=True)
    except urllib.error.HTTPError as e:
        print("import failed", e.code, e.read().decode(errors="replace")[:800], file=sys.stderr)
        return 1
    print(json.dumps(rep, indent=2, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
