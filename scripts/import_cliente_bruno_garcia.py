#!/usr/bin/env python3
"""Import conversa-Bruno Garcia.md into ImpaMind (project + vault + memories)."""

from __future__ import annotations

import json
import os
import re
import time
import urllib.error
import urllib.request
from collections import defaultdict
from datetime import datetime
from http.cookiejar import CookieJar
from pathlib import Path

BASE = "http://localhost:30303"
ADMIN_PASSWORD = os.environ.get("IMPAMIND_ADMIN_PASSWORD", "impamind-admin-local")
SOURCE = Path(r"D:\Users\jorra\OneDrive\Documentos\gitea\impaMind\conversa-Bruno Garcia.md")
PHONE = "554199952900"
SLUG = f"cliente-bruno-garcia-{PHONE}"
PROJECT_NAME = "Cliente Bruno Garcia"

MONTHS = {
    "janeiro": 1,
    "fevereiro": 2,
    "março": 3,
    "marco": 3,
    "abril": 4,
    "maio": 5,
    "junho": 6,
    "julho": 7,
    "agosto": 8,
    "setembro": 9,
    "outubro": 10,
    "novembro": 11,
    "dezembro": 12,
}
DATE_RE = re.compile(
    r"^(\d{1,2})\s+de\s+(janeiro|fevereiro|março|marco|abril|maio|junho|julho|agosto|setembro|outubro|novembro|dezembro)\s+de\s+(\d{4})$",
    re.I,
)
TIME_RE = re.compile(r"^\d{1,2}:\d{2}$")
STATUS_RE = re.compile(r"alterou o status", re.I)

# Secrets já gravados no cofre na importação — não deixar plaintext no repo.
# Reimport: extrair de novo do .md e PUT no vault (valores ficam só lá).
VAULT = [
    {"key": "whatsapp-phone", "label": "WhatsApp Bruno Garcia", "hint": "+55 (41) 9995-2900", "value": PHONE},
    {"key": "bag0412-admin", "label": "bag0412 / Portainer admin", "hint": "ver cofre", "value": ""},
    {"key": "bruno-gmail", "label": "Gmail Bruno", "hint": "brunoagarcia88@gmail.com", "value": ""},
    {"key": "cybersac-hub-admin", "label": "Cybersac Hub admin", "hint": "hub.cybersac.cloud", "value": ""},
    {"key": "evolution-apikey", "label": "Evolution / EvoGo API key", "hint": "evogo.cybersac.cloud", "value": ""},
    {"key": "docker-swarm-join", "label": "Docker swarm join token", "hint": "swarm", "value": ""},
    {"key": "gmail-app-unicesumar", "label": "Gmail app Unicesumar", "hint": "financeiro.unicesumar…", "value": ""},
    {"key": "supabase-panel", "label": "Supabase painel Studio", "hint": "supa.cybersac.cloud", "value": ""},
    {"key": "supabase-postgres", "label": "Supabase PostgreSQL", "hint": "postgres", "value": ""},
    {"key": "twilio-auth-token", "label": "Twilio auth token", "hint": "Twilio", "value": ""},
]

SECRET_SCRUB = [
    (re.compile(r"(?i)Mfcd\d+!!Mfcd\d+!!"), "[vault:" + SLUG + "/bag0412-admin]"),
    (re.compile(r"(?i)cybersacadmin123"), "[vault:" + SLUG + "/cybersac-hub-admin]"),
    (re.compile(r"48sd7gad7g8Adfsfs44S4d85s4f5s58cc7f45f"), "[vault:" + SLUG + "/evolution-apikey]"),
    (re.compile(r"SWMTKN-1-[A-Za-z0-9-]+"), "[vault:" + SLUG + "/docker-swarm-join]"),
    (re.compile(r"(?i)avrr\s+hncg\s+vjfe\s+bjtk"), "[vault:" + SLUG + "/gmail-app-unicesumar]"),
    (re.compile(r"jAWQ4zPt1xhUx35j"), "[vault:" + SLUG + "/supabase-panel]"),
    (re.compile(r"5W7qVaBACPCc3xluSn1V2UMC"), "[vault:" + SLUG + "/supabase-postgres]"),
    (re.compile(r"54a22c9a9d0a9a9243dc0c215525f769"), "[vault:" + SLUG + "/twilio-auth-token]"),
    (
        re.compile(
            r"(?i)\b(password|passwd|pwd|senha|secret|api[_-]?key|apikey|access[_-]?token|client[_-]?secret|auth[_-]?token)\b([\"']?\s*[:=]\s*[\"']?)([^\s\"',;]{6,})"
        ),
        r"\1\2[REDACTED]",
    ),
    (re.compile(r"(?i)login\s*:\s*\S+"), "login: [REDACTED_OR_SEE_VAULT]"),
    (re.compile(r"(?i)ADMIN_PASSWORD=\S+"), "ADMIN_PASSWORD=[REDACTED]"),
    (re.compile(r"(?i)ADMIN_EMAIL=\S+"), "ADMIN_EMAIL=[see vault]"),
]


class Client:
    def __init__(self, base: str):
        self.base = base.rstrip("/")
        self.cj = CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.cj)
        )

    def request(self, method: str, path: str, body: dict | None = None):
        data = None
        headers = {"Accept": "application/json", "Connection": "close"}
        if body is not None:
            data = json.dumps(body, ensure_ascii=False).encode("utf-8")
            headers["Content-Type"] = "application/json; charset=utf-8"
        last = None
        for attempt in range(6):
            req = urllib.request.Request(
                self.base + path, data=data, headers=headers, method=method
            )
            try:
                with self.opener.open(req, timeout=60) as resp:
                    raw = resp.read()
                    return json.loads(raw.decode("utf-8")) if raw else {}
            except urllib.error.HTTPError as e:
                err = e.read().decode("utf-8", errors="replace")
                raise RuntimeError(f"{method} {path} -> {e.code}: {err}") from e
            except Exception as e:
                last = e
                time.sleep(1.2 * (attempt + 1))
                try:
                    self.login(ADMIN_PASSWORD)
                except Exception:
                    pass
        raise RuntimeError(str(last)) from last

    def login(self, password: str):
        return self.request("POST", "/auth/login", {"password": password})


def scrub(text: str) -> str:
    out = text
    for pat, repl in SECRET_SCRUB:
        out = pat.sub(repl, out)
    # kill password_assignment leftovers
    lines = []
    assign = re.compile(
        r"(?i).*\b(password|passwd|pwd|senha|secret|api[_-]?key|apikey|access[_-]?token|client[_-]?secret|auth[_-]?token)\b[\"']?\s*[:=].*"
    )
    for line in out.splitlines():
        if assign.match(line):
            lines.append("[linha com credencial removida — ver cofre]")
        else:
            lines.append(line)
    return "\n".join(lines)


def parse_chat(path: Path) -> list[dict]:
    lines = path.read_text(encoding="utf-8").splitlines()
    msgs: list[dict] = []
    current_date = "27/04/2026"
    last_time = "00:00"
    buf: list[str] = []
    speaker = "Desconhecido"

    def flush():
        nonlocal buf, speaker
        text = "\n".join(buf).strip()
        buf = []
        if not text:
            return
        if STATUS_RE.search(text) and len(text) < 120:
            return
        if text in ("Pegar", "Transferir", "Reabrir", "Resolvida", "Aberta", "[Botão]"):
            return
        low = text.lower()
        sp = speaker
        if text.startswith("JORRANDESON") or "jorrandeson" in low[:40]:
            sp = "Impa 365"
            # strip name prefix line
            parts = text.split("\n", 1)
            if parts[0].upper().startswith("JORRANDESON"):
                text = parts[1].strip() if len(parts) > 1 else text
        elif "bruno" in low[:30]:
            sp = "Bruno Garcia"
        msgs.append(
            {
                "date": current_date,
                "time": last_time,
                "speaker": sp,
                "text": text,
                "source": "conversa-md",
            }
        )

    for line in lines:
        s = line.strip()
        dm = DATE_RE.match(s)
        if dm:
            flush()
            d, mon, y = int(dm.group(1)), MONTHS[dm.group(2).lower()], int(dm.group(3))
            current_date = f"{d:02d}/{mon:02d}/{y}"
            continue
        if TIME_RE.match(s):
            flush()
            last_time = s
            continue
        if s.startswith("JORRANDESON"):
            flush()
            speaker = "Impa 365"
            buf.append(line)
            continue
        buf.append(line)
    flush()
    # drop pure empty / emoji-only noise lightly
    kept = []
    for m in msgs:
        b = m["text"].strip()
        if not b or b in ("🔄", "1️⃣", "Teste"):
            continue
        kept.append(m)
    return kept


def parse_date(d: str) -> datetime:
    for fmt in ("%d/%m/%Y", "%d/%m/%y"):
        try:
            return datetime.strptime(d, fmt)
        except ValueError:
            continue
    return datetime(1970, 1, 1)


def chunk_text(s: str, max_len: int = 55000) -> list[str]:
    if len(s) <= max_len:
        return [s]
    return [s[i : i + max_len] for i in range(0, len(s), max_len)]


def day_digest(day: str, messages: list[dict]) -> list[tuple[str, str, list[str]]]:
    messages = sorted(messages, key=lambda m: (m["time"], m["speaker"]))
    lines = []
    for m in messages:
        body = m["text"].replace("\n", " | ").strip()
        if len(body) > 480:
            body = body[:480] + "…"
        lines.append(f"{m['time']} {m['speaker']}: {body}")
    header = (
        f"Digest conversa suporte — {day}\n"
        f"Mensagens: {len(messages)}\n"
        f"Cliente: Bruno Garcia ({PHONE})\n"
        f"Segredos redigidos → vault:{SLUG}/*\n\n"
    )
    body = scrub("\n".join(lines))
    full = header + body
    tags = ["conversa", "digest", "bruno-garcia", day.replace("/", "-")]
    out = []
    for i, ch in enumerate(chunk_text(full)):
        title = f"Conversa {day}" if i == 0 else f"Conversa {day} (parte {i+1})"
        out.append((title, ch, tags))
    return out


def topic_memories(msgs: list[dict]) -> list[dict]:
    topics = {
        "servidor-vps-hosteg": re.compile(r"(?i)servidor|vps|hosteg|hostinger|swarm"),
        "cybersac-stack": re.compile(r"(?i)cybersac|portainer|hub\.cybersac|waha|evogo"),
        "n8n-automacao": re.compile(r"(?i)n8n|automa[cç][aã]o|webhook|fluxo"),
        "supabase": re.compile(r"(?i)supabase|postgres|supa\.cybersac"),
        "whatsapp-api-meta": re.compile(r"(?i)whatsapp|waha|meta|oficial|inst[aâ]ncia"),
        "pagamentos-pix": re.compile(r"(?i)pix|pag(ar|ou|amento)|hosteg|97|mensal"),
        "chatflow-clinicaai": re.compile(r"(?i)chatflow|clinicaai|omnichannel"),
        "emails-acessos": re.compile(r"(?i)gmail|admin@|login|email"),
    }
    out = []
    for key, pat in topics.items():
        hits = [m for m in msgs if pat.search(m["text"])]
        if not hits:
            continue
        sample = hits[:: max(1, len(hits) // 35)][:35]
        lines = [
            f"{m['date']} {m['time']} {m['speaker']}: {m['text'].replace(chr(10), ' | ')[:260]}"
            for m in sample
        ]
        content = scrub(
            f"Tema: {key}\nOcorrencias: {len(hits)}\nAmostra:\n\n" + "\n".join(lines)
        )
        out.append(
            {
                "kind": "knowledge",
                "tier": 2,
                "title": f"Tema — {key}",
                "content": content,
                "tags": ["tema", key, "bruno-garcia"],
            }
        )
    return out


def build_memories(msgs: list[dict]) -> list[dict]:
    by_day: dict[str, list] = defaultdict(list)
    for m in msgs:
        by_day[m["date"]].append(m)
    days = sorted(by_day.keys(), key=parse_date)
    d0, d1 = days[0], days[-1]
    speakers = defaultdict(int)
    for m in msgs:
        speakers[m["speaker"]] += 1

    memories: list[dict] = [
        {
            "kind": "knowledge",
            "tier": 1,
            "title": "Perfil — Cliente Bruno Garcia",
            "content": (
                f"Cliente: Bruno Garcia\n"
                f"Projeto: {SLUG}\n"
                f"WhatsApp: vault:{SLUG}/whatsapp-phone (+55 41 9995-2900 → {PHONE})\n"
                f"Emails citados: brunoagarcia88@gmail.com, admin@bag0412.com, admin@cybersac.cloud, "
                f"financeiro.unicesumarcampolargo@gmail.com, projetoimpa@gmail.com\n"
                f"Periodo: {d0} → {d1} ({len(days)} dias)\n"
                f"Mensagens indexadas: {len(msgs)}\n"
                f"Falantes: "
                + ", ".join(f"{k} ({v})" for k, v in sorted(speakers.items(), key=lambda x: -x[1]))
                + "\n"
                f"Stack citada: Cybersac (hub/portainer/waha/evogo/supa), n8n.bag0412.com, Hosteg/Hostinger VPS, "
                f"ChatFlow, ClinicaAI, Docker Swarm.\n"
                f"Credenciais: somente cofre vault:{SLUG}/*"
            ),
            "tags": ["perfil", "cliente", "bruno-garcia"],
        },
        {
            "kind": "architecture",
            "tier": 1,
            "title": "Fontes importadas — Bruno Garcia",
            "content": (
                f"Importacao de {SOURCE.name} ({SOURCE.stat().st_size} bytes).\n"
                f"Formato: export suporte (datas 'DD de mes de YYYY' + horarios).\n"
                f"Slug: {SLUG}\n"
                f"Digests diarios + temas + URLs + acordos. Segredos no cofre."
            ),
            "tags": ["import", "fonte"],
        },
        {
            "kind": "business_rule",
            "tier": 1,
            "title": "Comercial — ChatFlow R$97/mes citado",
            "content": (
                "Em conversa (abr/2026) Impa apresentou ChatFlow omnichannel a R$97/mes.\n"
                "Links de pagamento Impa: pay.impa365.com (planos/vitrine citados na conversa).\n"
                "Hosteg promocode IMPA65 / painel.hosteg.com.br tambem citados."
            ),
            "tags": ["comercial", "chatflow"],
            "rationale": "Valor e produto citados explicitamente na conversa.",
        },
        {
            "kind": "knowledge",
            "tier": 2,
            "title": "Catalogo de URLs — Bruno Garcia",
            "content": scrub(
                "URLs na conversa:\n\n"
                + "\n".join(
                    f"- {u}"
                    for u in sorted(
                        set(
                            re.findall(
                                r"https?://[^\s<>\")\]]+",
                                "\n".join(m["text"] for m in msgs),
                            )
                        )
                    )
                )
            ),
            "tags": ["urls", "referencia"],
        },
        {
            "kind": "knowledge",
            "tier": 1,
            "title": "Acessos — refs no cofre (Bruno)",
            "content": (
                "Credenciais encontradas na conversa e movidas ao cofre:\n"
                f"- WhatsApp: vault:{SLUG}/whatsapp-phone\n"
                f"- bag0412 / Portainer: vault:{SLUG}/bag0412-admin\n"
                f"- Gmail Bruno: vault:{SLUG}/bruno-gmail\n"
                f"- Cybersac Hub: vault:{SLUG}/cybersac-hub-admin\n"
                f"- Evolution API key: vault:{SLUG}/evolution-apikey\n"
                f"- Docker swarm join: vault:{SLUG}/docker-swarm-join\n"
                f"- Gmail app Unicesumar: vault:{SLUG}/gmail-app-unicesumar\n"
                f"- Supabase painel: vault:{SLUG}/supabase-panel\n"
                f"- Supabase Postgres: vault:{SLUG}/supabase-postgres\n"
                f"- Twilio auth token: vault:{SLUG}/twilio-auth-token\n"
                "Nunca colar segredo em memoria de texto."
            ),
            "tags": ["credenciais", "cofre"],
        },
    ]

    # monthly
    by_month: dict[str, list] = defaultdict(list)
    for m in msgs:
        dt = parse_date(m["date"])
        by_month[f"{dt.year}-{dt.month:02d}"].append(m)
    for month, items in sorted(by_month.items()):
        sp = defaultdict(int)
        for m in items:
            sp[m["speaker"]] += 1
        memories.append(
            {
                "kind": "knowledge",
                "tier": 2,
                "title": f"Resumo mensal {month}",
                "content": (
                    f"Resumo {month}: {len(items)} msgs, {len({m['date'] for m in items})} dias.\n"
                    + "Falantes: "
                    + ", ".join(f"{k}={v}" for k, v in sorted(sp.items(), key=lambda x: -x[1]))
                    + "\nVer digests diarios."
                ),
                "tags": ["resumo", "mensal", month],
            }
        )

    memories.extend(topic_memories(msgs))

    for day in days:
        for title, content, tags in day_digest(day, by_day[day]):
            memories.append(
                {
                    "kind": "note",
                    "tier": 3,
                    "title": title,
                    "content": content,
                    "tags": tags,
                }
            )
    return memories


def main():
    print("Parsing…")
    msgs = parse_chat(SOURCE)
    print(f"msgs={len(msgs)}")
    by_day: dict[str, list] = defaultdict(list)
    for m in msgs:
        by_day[m["date"]].append(m)
    days = sorted(by_day.keys(), key=parse_date)
    print(f"days={len(days)} {days[0]} → {days[-1]}")

    c = Client(BASE)
    for attempt in range(5):
        try:
            c.login(ADMIN_PASSWORD)
            break
        except Exception as e:
            print("login retry", e)
            time.sleep(1.5)
    else:
        raise SystemExit("login failed")

    try:
        proj = c.request(
            "POST",
            "/api/projects",
            {"slug": SLUG, "name": PROJECT_NAME, "review_policy": "balanced"},
        )
        print("project", proj.get("slug"))
    except Exception as e:
        print("project", e)
        try:
            print("exists", c.request("GET", f"/api/projects/{SLUG}").get("slug"))
        except Exception:
            raise

    for v in VAULT:
        if not v.get("value"):
            print("vault skip empty", v["key"])
            continue
        for attempt in range(3):
            try:
                out = c.request(
                    "PUT",
                    f"/api/vault/{v['key']}?project={SLUG}&scope=project",
                    {
                        "label": v["label"],
                        "hint": v["hint"],
                        "value": v["value"],
                        "project": SLUG,
                        "scope": "project",
                    },
                )
                print("vault", out.get("ref"))
                break
            except Exception as e:
                print("vault retry", v["key"], e)
                time.sleep(1)

    memories = build_memories(msgs)
    # idempotent skip
    existing = set()
    offset = 0
    while True:
        batch = c.request(
            "GET",
            f"/api/memories?project={SLUG}&scope=project&limit=100&offset={offset}",
        )
        items = batch.get("items") or []
        if not items:
            break
        for it in items:
            existing.add(it.get("title", ""))
        offset += len(items)
        if offset >= batch.get("total", 0):
            break
    print(f"existing={len(existing)} to_create={len(memories)}")

    ok = skip = fail = 0
    for i, mem in enumerate(memories, 1):
        if mem["title"] in existing:
            skip += 1
            continue
        mem = dict(mem)
        mem["content"] = scrub(mem["content"])
        for attempt in range(6):
            try:
                payload = {
                    "project": SLUG,
                    "scope": "project",
                    "kind": mem.get("kind", "knowledge"),
                    "title": mem["title"],
                    "content": mem["content"],
                    "tier": mem.get("tier", 2),
                    "tags": mem.get("tags", []),
                    "approve": True,
                    "user_stated": True,
                }
                if mem.get("rationale"):
                    payload["rationale"] = mem["rationale"]
                created = c.request("POST", "/api/memories", payload)
                ok += 1
                existing.add(mem["title"])
                if (ok + skip) % 20 == 0 or i == len(memories):
                    print(f"  {i}/{len(memories)} ok={ok} skip={skip} fail={fail}")
                time.sleep(0.12)
                break
            except Exception as e:
                err = str(e)
                if "secret_detected" in err:
                    mem["content"] = scrub(
                        re.sub(
                            r"(?i).*\b(senha|password|apikey|api[_-]?key|token)\b.*",
                            "[scrubbed]",
                            mem["content"],
                        )
                    )
                    mem["content"] = re.sub(
                        r"\b[A-Fa-f0-9]{32,}\b", "[REDACTED_HEX]", mem["content"]
                    )
                if attempt == 5:
                    fail += 1
                    print(f"FAIL {i} {mem['title'][:50]}: {err[:180]}")
                else:
                    time.sleep(0.5 * (attempt + 1))

    listed = c.request("GET", f"/api/memories?project={SLUG}&scope=project&limit=1")
    vault = c.request("GET", f"/api/vault?project={SLUG}&scope=project")
    print(
        "DONE",
        json.dumps(
            {
                "slug": SLUG,
                "ok": ok,
                "skip": skip,
                "fail": fail,
                "total": listed.get("total"),
                "vault": len(vault.get("entries", [])),
                "days": len(days),
            },
            ensure_ascii=False,
        ),
    )


if __name__ == "__main__":
    main()
