#!/usr/bin/env python3
"""Import WhatsApp export for Cliente Alberto Lopes into ImpaMind."""

from __future__ import annotations

import json
import re
import time
import urllib.error
import urllib.request
from collections import defaultdict
from datetime import datetime
from http.cookiejar import CookieJar
from pathlib import Path

BASE = "http://localhost:30303"
ADMIN_PASSWORD = __import__("os").environ.get("IMPAMIND_ADMIN_PASSWORD", "impamind-admin-local")
SOURCE_DIR = Path(r"C:\Users\jorra\Downloads\A52008D32248083F55F2A79D2DD42315")
FOLDER_ID = "A52008D32248083F55F2A79D2DD42315"
PHONE = "5521975939307"
SLUG = "cliente-alberto-lopes-5521975939307"
PROJECT_NAME = "Cliente Alberto Lopes"

WA_FILE = SOURCE_DIR / "Conversa do WhatsApp com Alberto Lopes.txt"
CONV_FILE = SOURCE_DIR / "CONVERSA.txt"

MSG_RE = re.compile(
    r"^(\d{1,2}/\d{1,2}/\d{2,4})\s+(\d{1,2}:\d{2})\s+-\s+([^:]+):\s*(.*)$"
)
DATE_HEADER_RE = re.compile(
    r"^(\d{1,2})\s+de\s+(janeiro|fevereiro|março|marco|abril|maio|junho|julho|agosto|setembro|outubro|novembro|dezembro)\s+de\s+(\d{4})$",
    re.I,
)
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
TIME_ONLY = re.compile(r"^\d{1,2}:\d{2}$")

# Known secrets → vault keys (values filled at extract time)
VAULT_SPECS = [
    {
        "key": "zcall-impa365-apikey",
        "label": "ZCall Impa365 API key",
        "hint": "https://zcall.impa365.com/",
        "pattern": re.compile(
            r"Apikey:\s*(908b52b3038892668346171e6c5444fce734b339a2fc486ceea7d119a3c5302c)",
            re.I,
        ),
    },
    {
        "key": "zcall-blackatende-apikey",
        "label": "ZCall BlackAtende API key",
        "hint": "https://zcall.blackatende.com/",
        "pattern": re.compile(
            r"Apikey:\s*\n?\s*(3faaedd372000b17c4d8db0425acba71a7262e3f1f9766637e8cea10fa83d274)",
            re.I,
        ),
    },
    {
        "key": "blackatende-login-password",
        "label": "BlackAtende email/senha",
        "hint": "contato@blackatende.com",
        "pattern": re.compile(
            r"Email:\s*contato@blackatende\.com\s*\nSenha:\s*([^\s]+)",
            re.I,
        ),
        "value_from": "pair",
    },
    {
        "key": "pix-impa365-email",
        "label": "PIX Impa365",
        "hint": "chave email",
        "literal": "pix@impa365.com",
    },
    {
        "key": "pix-chave-cpf-09824496580",
        "label": "PIX chave numerica citada na conversa",
        "hint": "enviada por Impa 365 em 14/06/2026",
        "literal": "09824496580",
    },
    {
        "key": "whatsapp-export-folder-id",
        "label": "ID pasta export WhatsApp",
        "hint": "Downloads/A52008...",
        "literal": FOLDER_ID,
    },
    {
        "key": "whatsapp-phone",
        "label": "WhatsApp telefone Alberto Lopes",
        "hint": "numero E.164 sem +",
        "literal": PHONE,
    },
]

REDACTIONS = [
    (
        re.compile(
            r"(?i)(Apikey:\s*)908b52b3038892668346171e6c5444fce734b339a2fc486ceea7d119a3c5302c"
        ),
        r"\1vault:" + SLUG + "/zcall-impa365-apikey",
    ),
    (
        re.compile(
            r"(?i)(Apikey:\s*)3faaedd372000b17c4d8db0425acba71a7262e3f1f9766637e8cea10fa83d274"
        ),
        r"\1vault:" + SLUG + "/zcall-blackatende-apikey",
    ),
    (
        re.compile(
            r"(?i)Email:\s*contato@blackatende\.com\s*\n?Senha:\s*[^\s]+"
        ),
        "Login BlackAtende: vault:" + SLUG + "/blackatende-login-password",
    ),
    # Hex-ish API keys after Apikey label (any)
    (
        re.compile(r"(?i)\b(api[_-]?key|apikey)\b([\"']?\s*[:=]\s*[\"']?)([A-Fa-f0-9]{32,})"),
        r"\1\2[REDACTED]",
    ),
    # Generic password_assignment pattern (must leave <8 chars after := or remove value)
    (
        re.compile(
            r"(?i)\b(password|passwd|pwd|senha|secret|api[_-]?key|access[_-]?token|client[_-]?secret)\b([\"']?\s*[:=]\s*[\"']?)([^\s\"',;]{8,})"
        ),
        r"\1\2[REDACTED]",
    ),
    # Also catch "Login: https://..." followed by Apikey on next lines already handled
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
        req = urllib.request.Request(
            self.base + path, data=data, headers=headers, method=method
        )
        try:
            with self.opener.open(req, timeout=60) as resp:
                raw = resp.read()
                if not raw:
                    return {"ok": True, "status": resp.status}
                return json.loads(raw.decode("utf-8"))
        except urllib.error.HTTPError as e:
            err = e.read().decode("utf-8", errors="replace")
            raise RuntimeError(f"{method} {path} -> {e.code}: {err}") from e

    def login(self, password: str):
        return self.request("POST", "/auth/login", {"password": password})


def parse_wa(path: Path) -> list[dict]:
    text = path.read_text(encoding="utf-8")
    out: list[dict] = []
    cur = None
    for line in text.splitlines():
        m = MSG_RE.match(line)
        if m:
            if cur:
                out.append(cur)
            cur = {
                "date": m.group(1),
                "time": m.group(2),
                "speaker": m.group(3).strip(),
                "text": m.group(4),
                "source": "whatsapp-export",
            }
            continue
        if cur is not None:
            # system / non-message dated lines
            if re.match(r"^\d{1,2}/\d{1,2}/\d{2,4}\s+\d{1,2}:\d{2}\s+- ", line):
                if ":" not in line.split(" - ", 1)[-1][:100]:
                    out.append(cur)
                    cur = None
                    continue
            cur["text"] += "\n" + line
    if cur:
        out.append(cur)
    kept = []
    for m in out:
        b = m["text"].strip()
        if not b:
            continue
        if b.lower().startswith("aguardando mensagem"):
            continue
        kept.append(m)
    return kept


def parse_conversa(path: Path) -> list[dict]:
    """Parse informal CONVERSA.txt (date headers + time-only stamps)."""
    lines = path.read_text(encoding="utf-8").splitlines()
    msgs: list[dict] = []
    current_date = None
    buf: list[str] = []
    last_time = "00:00"

    def flush():
        nonlocal buf
        text = "\n".join(buf).strip()
        buf = []
        if not text or not current_date:
            return
        # skip pure media labels alone sometimes useful
        speaker = "Desconhecido"
        low = text.lower()
        if "jorrandeson" in low or text.startswith("JORRANDESON"):
            speaker = "Impa 365"
        elif text.startswith("Alberto") or "alberto" in low[:40]:
            speaker = "Alberto Lopes"
        msgs.append(
            {
                "date": current_date,
                "time": last_time,
                "speaker": speaker,
                "text": text,
                "source": "conversa-txt",
            }
        )

    for line in lines:
        dm = DATE_HEADER_RE.match(line.strip())
        if dm:
            flush()
            d, mon, y = int(dm.group(1)), MONTHS[dm.group(2).lower()], int(dm.group(3))
            current_date = f"{d:02d}/{mon:02d}/{y}"
            continue
        if TIME_ONLY.match(line.strip()):
            flush()
            last_time = line.strip()
            continue
        if current_date is None:
            # preamble before first date — bucket as 24/04/2026 approx from content
            current_date = "24/04/2026"
        buf.append(line)
    flush()
    return msgs


def parse_date(d: str) -> datetime:
    for fmt in ("%d/%m/%Y", "%d/%m/%y"):
        try:
            return datetime.strptime(d, fmt)
        except ValueError:
            continue
    return datetime(1970, 1, 1)


def redact(text: str) -> str:
    out = text
    for pat, repl in REDACTIONS:
        out = pat.sub(repl, out)
    return out


def chunk_text(s: str, max_len: int = 55000) -> list[str]:
    if len(s) <= max_len:
        return [s]
    parts = []
    start = 0
    while start < len(s):
        parts.append(s[start : start + max_len])
        start += max_len
    return parts


def day_digest(day: str, messages: list[dict]) -> list[tuple[str, str, list[str]]]:
    """Return list of (title, content, tags)."""
    messages = sorted(messages, key=lambda m: (m["time"], m["speaker"]))
    lines = []
    for m in messages:
        body = m["text"].replace("\n", " | ").strip()
        if body in ("<Mídia oculta>", "<Midia oculta>", "<Mídia omitida>"):
            body = "[midia]"
        if len(body) > 500:
            body = body[:500] + "…"
        lines.append(f"{m['time']} {m['speaker']}: {body}")
    header = (
        f"Digest da conversa WhatsApp — {day}\n"
        f"Mensagens uteis: {len(messages)}\n"
        f"Fontes: {', '.join(sorted({m['source'] for m in messages}))}\n"
        f"Segredos redigidos → ver cofre vault:{SLUG}/*\n\n"
    )
    body = redact("\n".join(lines))
    full = header + body
    tags = ["conversa", "digest", "alberto-lopes", day.replace("/", "-")]
    chunks = chunk_text(full)
    out = []
    for i, ch in enumerate(chunks):
        title = f"Conversa {day}" if i == 0 else f"Conversa {day} (parte {i+1})"
        out.append((title, ch, tags))
    return out


def extract_vault_values(blob: str) -> dict[str, str]:
    values = {}
    for spec in VAULT_SPECS:
        if "literal" in spec:
            values[spec["key"]] = spec["literal"]
            continue
        m = spec["pattern"].search(blob)
        if not m:
            continue
        if spec.get("value_from") == "pair":
            values[spec["key"]] = f"email=contato@blackatende.com; senha={m.group(1)}"
        else:
            values[spec["key"]] = m.group(1)
    return values


def topic_memories(msgs: list[dict]) -> list[dict]:
    topics = {
        "api-oficial-whatsapp": re.compile(
            r"(?i)api oficial|matriz|meta\b|cloud api|waba"
        ),
        "zcall-ligacoes": re.compile(r"(?i)zcall|blackatende|liga[cç][aã]o|exten"),
        "automacao-n8n": re.compile(r"(?i)n8n|automa[cç][aã]o|fluxo|webhook"),
        "crm-impa-datacrazy": re.compile(r"(?i)crm|datacrazy|impa\s*ai|impa365"),
        "tokens-llm": re.compile(
            r"(?i)token|openai|claude|gpt|deepseek|omni.?router|nubiofy"
        ),
        "pagamentos-pix-suporte": re.compile(
            r"(?i)pix|pag(ar|ou|amento)|mensalidade|suporte mensal|reembolso|devolver"
        ),
        "leads-disparos": re.compile(r"(?i)lead|disparo|planilha|campanha"),
        "solucoes-verdes": re.compile(r"(?i)solu[cç][oõ]es verdes|erro meta"),
        "doutor-jose-agendamento": re.compile(
            r"(?i)doutor jos[eé]|agendamento|secretar"
        ),
        "entrega-sistema-prazo": re.compile(
            r"(?i)cad[eê] o sistema|devolver o (dinheiro|valor)|at[eé] amanh[aã]|entrega"
        ),
        "projeto-rede-50k": re.compile(
            r"(?i)50\s*mil|50k|rede social|v[ií]deo chamada"
        ),
        "hostinger-vps": re.compile(r"(?i)hostinger|vps|servidor"),
        "cal-com-agendamento": re.compile(r"(?i)cal\.com|anydesk|anydask"),
        "respondame-funis": re.compile(r"(?i)responda\.me|funil|vital[ií]cio"),
    }
    out = []
    for key, pat in topics.items():
        hits = [m for m in msgs if pat.search(m["text"])]
        if not hits:
            continue
        # sample up to 40 representative lines
        sample = hits[:: max(1, len(hits) // 40)][:40]
        lines = []
        for m in sample:
            body = m["text"].replace("\n", " | ")[:280]
            lines.append(f"{m['date']} {m['time']} {m['speaker']}: {body}")
        content = redact(
            f"Tema: {key}\nOcorrencias na conversa: {len(hits)}\n"
            f"Amostra cronologica (redigida):\n\n" + "\n".join(lines)
        )
        out.append(
            {
                "kind": "knowledge",
                "tier": 2,
                "title": f"Tema — {key}",
                "content": content,
                "tags": ["tema", key, "alberto-lopes"],
            }
        )
    return out


def commercial_memories() -> list[dict]:
    return [
        {
            "kind": "business_rule",
            "tier": 1,
            "title": "Acordo comercial — suporte mensal",
            "content": (
                "Combinado na conversa (30/07/2026): suporte mensal ~R$297 "
                "(Alberto perguntou se era 300; Impa 365 confirmou 297).\n"
                "PIX Impa365: vault:"
                + SLUG
                + "/pix-impa365-email\n"
                "Outras chaves PIX citadas: vault:"
                + SLUG
                + "/pix-chave-cpf-09824496580\n"
                "Historico de valores pontuais: R$120, pedidos de 500 no credito/"
                "Asaas/Nubank, splits de projeto (ex.: 400 Alberto / 700 Impa / 700 tokens)."
            ),
            "tags": ["comercial", "suporte", "pix"],
            "rationale": "Valores e recorrencia citados explicitamente na conversa.",
        },
        {
            "kind": "business_rule",
            "tier": 1,
            "title": "Tabela API WhatsApp (datacrazy) citada",
            "content": (
                "Em 26/06/2026 Impa 365 passou valores API:\n"
                "- Individual: 400\n- A partir de 3: 300 cada\n- Acima de 5: 250 cada\n"
                "Inclui conexao API oficial no datacrazy.\n"
                "Tema recorrente: API oficial vs nao-oficial, matriz, Meta."
            ),
            "tags": ["comercial", "api", "whatsapp"],
            "rationale": "Tabela de preco enviada na conversa.",
        },
        {
            "kind": "decision",
            "tier": 1,
            "title": "Status critico entrega sistema (out/2026)",
            "content": (
                "Em 11/10/2026 Alberto cobrou entrega do sistema com prazo 'ate amanha', "
                "mencionou ~1 mes de atraso (Lorram), ameacou devolucao do valor se nao "
                "entregar, e disse que nao vai perder mais cliente por falta de entrega. "
                "Tambem falou que o outro numero voltou (API oficial matriz) e perguntou "
                "se a IA vai funcionar nas duas. Ofereceu mandar R$80 no mesmo dia.\n"
                "Prioridade: esclarecer status de entrega / reembolso / API matriz."
            ),
            "tags": ["urgencia", "entrega", "risco"],
            "rationale": "Ultimas mensagens da exportacao; risco comercial alto.",
        },
    ]


def profile_memory(msgs: list[dict], days: int) -> dict:
    speakers = defaultdict(int)
    for m in msgs:
        speakers[m["speaker"]] += 1
    d0 = min(msgs, key=lambda m: parse_date(m["date"]))["date"]
    d1 = max(msgs, key=lambda m: parse_date(m["date"]))["date"]
    return {
        "kind": "knowledge",
        "tier": 1,
        "title": "Perfil — Cliente Alberto Lopes",
        "content": (
            f"Cliente: Alberto Lopes\n"
            f"Projeto ImpaMind: {SLUG}\n"
            f"ID pasta export WhatsApp: vault:{SLUG}/whatsapp-export-folder-id "
            f"({FOLDER_ID})\n"
            f"Periodo coberto: {d0} → {d1} ({days} dias com mensagem)\n"
            f"Mensagens indexadas: {len(msgs)}\n"
            f"Participantes principais: "
            + ", ".join(f"{k} ({v})" for k, v in sorted(speakers.items(), key=lambda x: -x[1])[:8])
            + "\n"
            "Contatos compartilhados na pasta:\n"
            "- Advocacia Doutor Paulino / Escritorio Paulino Advocacia: +55 21 95953-1109\n"
            "- Ellan | TI - LIGACAO: +55 91 98296-2623\n"
            "Emails citados: contato@blackatende.com, grupogoverne@gmail.com, "
            "pix@impa365.com, projetoimpa@gmail.com, rafahdbimpa@rgmulti.com.br\n"
            "Sistemas citados: ZCall, BlackAtende, CRM Impa / DataCrazy, n8n, "
            "API oficial WhatsApp/Meta, OmniRouter, Hostinger/VPS, cal.com, Asaas.\n"
            "Credenciais: somente no cofre (vault:"
            + SLUG
            + "/*). Nunca colar senha/apikey em memoria."
        ),
        "tags": ["perfil", "cliente", "alberto-lopes"],
    }


def contacts_memory() -> dict:
    return {
        "kind": "knowledge",
        "tier": 2,
        "title": "Contatos VCF da pasta do cliente",
        "content": (
            "Arquivos VCF no export:\n"
            "1) Advocacia Doutor paulino.vcf — FN: Advocacia Doutor paulino; "
            "X-WA-BIZ-NAME: Escritorio Paulino Advocacia; TEL waid=5521959531109 "
            "(+55 21 95953-1109)\n"
            "2) Ellan | TI - LIGACAO.vcf — FN: Ellan | TI - LIGACAO; "
            "TEL waid=559182962623 (+55 91 98296-2623)\n"
            "Fonte: pasta Downloads/"
            + FOLDER_ID
        ),
        "tags": ["contatos", "vcf"],
    }


def urls_memory(msgs: list[dict]) -> dict:
    blob = "\n".join(m["text"] for m in msgs)
    urls = sorted(set(re.findall(r"https?://[^\s<>\"']+", blob)))
    # strip trailing punctuation
    clean = []
    for u in urls:
        clean.append(u.rstrip(").,];"))
    urls = sorted(set(clean))
    body = "URLs citadas na conversa (" + str(len(urls)) + "):\n\n" + "\n".join(
        f"- {u}" for u in urls[:200]
    )
    return {
        "kind": "knowledge",
        "tier": 3,
        "title": "Catalogo de URLs da conversa",
        "content": body[:60000],
        "tags": ["urls", "referencia"],
    }


def monthly_memories(msgs: list[dict]) -> list[dict]:
    by_month: dict[str, list] = defaultdict(list)
    for m in msgs:
        dt = parse_date(m["date"])
        by_month[f"{dt.year}-{dt.month:02d}"].append(m)
    out = []
    for month, items in sorted(by_month.items()):
        speakers = defaultdict(int)
        for m in items:
            speakers[m["speaker"]] += 1
        # keyword counts
        blob = "\n".join(m["text"] for m in items).lower()
        kws = {
            k: blob.count(k)
            for k in [
                "api",
                "pix",
                "ia",
                "lead",
                "sistema",
                "n8n",
                "crm",
                "token",
                "oficial",
                "erro",
            ]
        }
        top = ", ".join(f"{k}:{v}" for k, v in sorted(kws.items(), key=lambda x: -x[1]) if v)
        content = (
            f"Resumo mensal {month}\n"
            f"Mensagens: {len(items)}\n"
            f"Dias ativos: {len({m['date'] for m in items})}\n"
            f"Por falante: "
            + ", ".join(f"{k}={v}" for k, v in sorted(speakers.items(), key=lambda x: -x[1]))
            + f"\nPalavras-chave: {top}\n"
            "Ver digests diarios Conversa DD/MM/AAAA neste projeto para detalhe."
        )
        out.append(
            {
                "kind": "knowledge",
                "tier": 2,
                "title": f"Resumo mensal {month}",
                "content": content,
                "tags": ["resumo", "mensal", month],
            }
        )
    return out


def create_project(c: Client):
    try:
        return c.request(
            "POST",
            "/api/projects",
            {"slug": SLUG, "name": PROJECT_NAME, "review_policy": "balanced"},
        )
    except RuntimeError as e:
        if "already" in str(e).lower() or "conflict" in str(e).lower() or "409" in str(e):
            return {"slug": SLUG, "exists": True}
        # list scopes to confirm
        scopes = c.request("GET", "/api/scopes")
        for s in scopes.get("scopes", []):
            if s.get("slug") == SLUG:
                return {"slug": SLUG, "exists": True, "id": s.get("id")}
        raise


def put_vault(c: Client, key: str, label: str, hint: str, value: str):
    return c.request(
        "PUT",
        f"/api/vault/{key}?project={SLUG}&scope=project",
        {
            "label": label,
            "hint": hint,
            "value": value,
            "project": SLUG,
            "scope": "project",
        },
    )


def create_memory(c: Client, mem: dict) -> dict:
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
    return c.request("POST", "/api/memories", payload)


def main():
    print("Parsing exports…")
    wa = parse_wa(WA_FILE)
    conv = parse_conversa(CONV_FILE)
    # Prefer WA for overlapping days; keep conversa-only days
    wa_days = {m["date"] for m in wa}
    conv_extra = [m for m in conv if m["date"] not in wa_days]
    # Also keep conversa messages on shared days if they look substantive and not dup
    msgs = wa + conv_extra
    print(f"WA msgs={len(wa)} CONVERSA extra={len(conv_extra)} total={len(msgs)}")

    by_day: dict[str, list] = defaultdict(list)
    for m in msgs:
        by_day[m["date"]].append(m)
    days_sorted = sorted(by_day.keys(), key=parse_date)
    print(f"days={len(days_sorted)} range={days_sorted[0]} → {days_sorted[-1]}")

    blob = "\n".join(m["text"] for m in msgs)
    vault_vals = extract_vault_values(blob)
    print("vault keys found:", sorted(vault_vals.keys()))

    c = Client(BASE)
    # retry login on Winsock hiccups
    for attempt in range(5):
        try:
            c.login(ADMIN_PASSWORD)
            break
        except Exception as e:
            print("login retry", attempt, e)
            time.sleep(1.5)
    else:
        raise SystemExit("login failed")

    proj = create_project(c)
    print("project:", proj)

    for spec in VAULT_SPECS:
        val = vault_vals.get(spec["key"]) or spec.get("literal")
        if not val:
            print("skip vault", spec["key"])
            continue
        for attempt in range(3):
            try:
                out = put_vault(c, spec["key"], spec["label"], spec["hint"], val)
                print("vault", out.get("ref", spec["key"]))
                break
            except Exception as e:
                print("vault retry", spec["key"], e)
                time.sleep(1)

    memories: list[dict] = []
    memories.append(profile_memory(msgs, len(days_sorted)))
    memories.append(contacts_memory())
    memories.extend(commercial_memories())
    memories.append(urls_memory(msgs))
    memories.extend(monthly_memories(msgs))
    memories.extend(topic_memories(msgs))
    memories.append(
        {
            "kind": "architecture",
            "tier": 1,
            "title": "Fontes importadas neste projeto",
            "content": (
                f"Importacao automatica da pasta {SOURCE_DIR}\n"
                f"- Conversa do WhatsApp com Alberto Lopes.txt ({WA_FILE.stat().st_size} bytes)\n"
                f"- CONVERSA.txt ({CONV_FILE.stat().st_size} bytes) — periodo abril/maio complementar\n"
                f"- VCFs: Advocacia Doutor paulino.vcf, Ellan TI LIGACAO.vcf\n"
                f"Slug padrao: cliente-{{nome}}-{{numero}} → {SLUG}\n"
                f"Digests diarios: um (ou mais) por dia com mensagem util.\n"
                f"Segredos apenas no cofre."
            ),
            "tags": ["import", "fonte", "meta"],
        }
    )

    for day in days_sorted:
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

    # Idempotent: skip titles already present
    existing_titles = set()
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
            existing_titles.add(it.get("title", ""))
        offset += len(items)
        if offset >= batch.get("total", 0):
            break
    print(f"already in project: {len(existing_titles)} titles")

    print(f"creating {len(memories)} memories…")
    ok = 0
    skip = 0
    fail = 0
    for i, mem in enumerate(memories, 1):
        if mem["title"] in existing_titles:
            skip += 1
            continue
        mem = dict(mem)
        mem["content"] = redact(mem["content"])
        for attempt in range(6):
            try:
                created = create_memory(c, mem)
                ok += 1
                existing_titles.add(mem["title"])
                if (ok + skip) % 20 == 0 or i == len(memories):
                    print(
                        f"  {i}/{len(memories)} ok={ok} skip={skip} fail={fail} "
                        f"last={created.get('id','')[:8]}"
                    )
                time.sleep(0.15)
                break
            except Exception as e:
                err = str(e)
                if "secret_detected" in err:
                    mem["content"] = re.sub(
                        r"(?i)\b(password|passwd|pwd|senha|secret|api[_-]?key|apikey|access[_-]?token|client[_-]?secret)\b([\"']?\s*[:=]\s*[\"']?)([^\s\"',;]+)",
                        r"\1\2[REDACTED]",
                        mem["content"],
                    )
                    # nuke long hex blobs that look like keys
                    mem["content"] = re.sub(r"\b[A-Fa-f0-9]{40,}\b", "[REDACTED_HEX]", mem["content"])
                if "10048" in err or "urlopen" in err:
                    time.sleep(1.5 * (attempt + 1))
                    # re-login / new opener
                    try:
                        c.login(ADMIN_PASSWORD)
                    except Exception:
                        pass
                if attempt == 5:
                    fail += 1
                    print(f"FAIL {i} {mem['title'][:60]}: {err[:220]}")
                else:
                    time.sleep(0.5 * (attempt + 1))

    # verify
    listed = c.request(
        "GET", f"/api/memories?project={SLUG}&scope=project&limit=1"
    )
    vault = c.request("GET", f"/api/vault?project={SLUG}&scope=project")
    print(
        "DONE",
        json.dumps(
            {
                "slug": SLUG,
                "memories_created_ok": ok,
                "memories_skipped": skip,
                "memories_failed": fail,
                "memories_total_api": listed.get("total"),
                "vault_entries": len(vault.get("entries", [])),
                "days": len(days_sorted),
            },
            ensure_ascii=False,
        ),
    )


if __name__ == "__main__":
    main()
