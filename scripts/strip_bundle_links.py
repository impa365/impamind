#!/usr/bin/env python3
"""Rewrite a .impamind ZIP with links cleared (same vault/passphrase)."""
from __future__ import annotations

import hashlib
import json
import sys
import zipfile
from pathlib import Path


def main() -> int:
    if len(sys.argv) < 3:
        print("usage: strip_bundle_links.py in.impamind out.impamind", file=sys.stderr)
        return 2
    src, dst = Path(sys.argv[1]), Path(sys.argv[2])
    with zipfile.ZipFile(src) as zin:
        man = json.loads(zin.read("manifest.json"))
        payload = json.loads(zin.read("payload.json"))
        salt = zin.read("vault.salt")
        sealed = zin.read("vault.sealed")
    n = len(payload.get("links") or [])
    payload["links"] = []
    payload_json = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    h = hashlib.sha256()
    h.update(payload_json)
    h.update(sealed)
    man["content_hash"] = h.hexdigest()
    man["counts"]["links"] = 0
    man_json = json.dumps(man, indent=2, ensure_ascii=False).encode("utf-8")
    dst.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(dst, "w", compression=zipfile.ZIP_DEFLATED) as zout:
        zout.writestr("manifest.json", man_json)
        zout.writestr("payload.json", payload_json)
        zout.writestr("vault.salt", salt)
        zout.writestr("vault.sealed", sealed)
    print(f"stripped {n} links → {dst} ({dst.stat().st_size} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
