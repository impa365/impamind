#!/usr/bin/env python3
"""Undo UTF-8 mojibake in static JS (PowerShell double-encode)."""
from pathlib import Path

FILES = [
    Path(__file__).resolve().parents[1] / "internal" / "web" / "static" / "app.js",
    Path(__file__).resolve().parents[1] / "internal" / "web" / "static" / "index.html",
    Path(__file__).resolve().parents[1] / "internal" / "web" / "static" / "app.css",
]

REPL = {
    "Ã¡": "á",
    "Ã ": "à",
    "Ã¢": "â",
    "Ã£": "ã",
    "Ã©": "é",
    "Ã¨": "è",
    "Ãª": "ê",
    "Ã­": "í",
    "Ã¬": "ì",
    "Ã®": "î",
    "Ã³": "ó",
    "Ã²": "ò",
    "Ã´": "ô",
    "Ãµ": "õ",
    "Ãº": "ú",
    "Ã¹": "ù",
    "Ã»": "û",
    "Ã§": "ç",
    "Ã‡": "Ç",
    "Ã‰": "É",
    "Ã": "Á",
    "Ã": "Í",
    "Ã“": "Ó",
    "Ãš": "Ú",
    "Ã±": "ñ",
    "Â·": "·",
    "Â ": " ",
    "â€¢": "•",
    "â€”": "—",
    "â€“": "–",
    "â€œ": '"',
    "â€\x9d": '"',
    "â€˜": "'",
    "â€™": "'",
    "â†": "←",
    "â†’": "→",
    "â€\x94": "—",
}


def fix_text(text: str) -> str:
    # Prefer byte-level undo when the whole file is one-layer mojibake.
    try:
        return text.encode("latin-1").decode("utf-8")
    except (UnicodeEncodeError, UnicodeDecodeError):
        pass
    out = text
    for src, dst in sorted(REPL.items(), key=lambda kv: len(kv[0]), reverse=True):
        out = out.replace(src, dst)
    out = out.replace("substituÃ\xadda", "substituída")
    out = out.replace("substituÃ­da", "substituída")
    while "Â" in out:
        out = out.replace("Â", "")
    return out


def main() -> None:
    for path in FILES:
        if not path.exists():
            continue
        original = path.read_text(encoding="utf-8")
        fixed = fix_text(original)
        if fixed == original:
            print(f"ok   {path.name}")
            continue
        path.write_text(fixed, encoding="utf-8")
        checks = ["Ligações", "memória", "Rótulo", "Versões", "administração"]
        hit = [c for c in checks if c in fixed]
        print(f"fix {path.name}: {', '.join(hit) or 'changed'}")


if __name__ == "__main__":
    main()
