# Subir o impamind no Portainer + Traefik

Objetivo: acesso só por **HTTPS no domínio** (`https://impamind.impa365.com` / `.cloud`).  
A porta `30303` **não** é publicada na VPS — só o Traefik (80/443) fala com o mundo.

**Stack pronta no padrão Impa365 (ImpaServer + labels Traefik iguais ao 9router):**  
[`stack.impa365.yml`](stack.impa365.yml) — cola direto no Portainer.

Stack genérica com variáveis: [`stack.yml`](stack.yml).

Uma réplica só. Duas réplicas no mesmo SQLite corrompem o banco.

## Pré-requisitos

1. Traefik v3 já rodando no Swarm, com entrypoints:
   - `web` → `:80`
   - `websecure` → `:443`
2. Rede overlay do Traefik (ex.: `traefik_public`) já criada e usada pelo Traefik.
3. Cert resolver configurado (ex.: `letsencrypt`).
4. DNS do domínio apontando para o IP da VPS (A/AAAA), **sem** abrir firewall para `30303`.

No firewall da VPS (ou security group), libere só:

- `22` (SSH, se precisar)
- `80` e `443` (Traefik)

**Não** abra `30303` para a internet.

## 1. Imagem

Construa e publique a imagem (registry privado, GHCR, etc.):

```bash
docker build -t ghcr.io/SEU_USER/impamind:1 .
docker push ghcr.io/SEU_USER/impamind:1
```

No Portainer, o nó precisa conseguir puxar essa imagem (`IMAGE` abaixo).

## 2. Stack no Portainer

1. **Stacks** → **Add stack**
2. Cole o conteúdo de [`stack.yml`](stack.yml) (ou Web Editor / Git)
3. Em **Environment variables**, use como base [`portainer.env.example`](portainer.env.example):

| Variável | Exemplo | Nota |
|---|---|---|
| `DOMAIN` | `memoria.impa365.cloud` | Host público (sem `https://`) |
| `TRAEFIK_NETWORK` | `traefik_public` | Nome da rede overlay do Traefik |
| `TRAEFIK_ENTRYPOINT` | `websecure` | Entrypoint HTTPS do Traefik |
| `TRAEFIK_HTTP_ENTRYPOINT` | `web` | Entrypoint HTTP (redirect) |
| `CERT_RESOLVER` | `letsencrypt` | Nome do certResolver no Traefik |
| `IMAGE` | `ghcr.io/SEU_USER/impamind:1` | Imagem publicada |
| `IMPAMIND_BOOTSTRAP_TOKEN` | (segredo ≥ 24 chars) | Vira o primeiro token de agente |
| `IMPAMIND_ADMIN_PASSWORD` | (senha ≥ 10 chars) | Login da UI |
| `IMPAMIND_SECRET` | (segredo longo) | Cookie de sessão |

Gere segredos:

```bash
openssl rand -hex 32
```

4. Deploy da stack.
5. Espere o healthcheck ficar verde (1 réplica).

## 3. O que o Traefik faz

Labels no serviço (já no `stack.yml`):

- `Host(\`DOMAIN\`)` no entrypoint HTTPS + TLS via `CERT_RESOLVER`
- Mesmo host no entrypoint HTTP → redirect permanente para HTTPS
- `loadbalancer.server.port=30303` **interno** (rede overlay)
- **Sem** `ports:` no Compose → nada escuta `IP:30303` na máquina

O app recebe:

- `IMPAMIND_PUBLIC_URL=https://DOMAIN` (OAuth, página Conectar, OpenAPI)
- `IMPAMIND_COOKIE_SECURE=true`
- `IMPAMIND_CORS_ORIGINS=https://DOMAIN`

## 4. Conferir

```bash
# Na VPS: 30303 NÃO deve aparecer em 0.0.0.0
ss -lntp | grep 30303 || echo "ok: porta nao publicada"

# De fora
curl -I https://memoria.impa365.cloud/health
```

Abra `https://DOMAIN/` (login admin) e `https://DOMAIN/conectar` para o MCP.

O token de agente **não** vai no git: fica em `IMPAMIND_TOKEN` na máquina do cliente.

## 5. Migrar dados (arquivo `.impamind`)

Clone portátil entre instâncias (local ↔ VPS). Spec: [`docs/impamind-format.md`](../docs/impamind-format.md).

1. **Na origem** (ex.: `http://localhost:30303`): Admin → **Clone .impamind**  
   - Passphrase forte (≥ 8 chars, confirme)  
   - Marque **Toda a instância** + **Incluir global**  
   - **Exportar .impamind** (baixa o ZIP)
2. **No destino** (ex.: `https://memoria.impa365.cloud`): faça deploy da imagem que já tem bundle API  
3. Admin → mesma passphrase → **Importar .impamind**  
   - Opcional: **Sobrescrever vault no import** se as keys já existirem
4. Valide projetos `cliente-*`, `credenciais-impa365`, entradas do Cofre

O arquivo **contém o cofre** (cifrado só com a passphrase). Não versionar no git; apague após a migração.

Via API (cookie admin):

```bash
# export
curl -sS -c cookies.txt -X POST http://localhost:30303/auth/login \
  -H 'Content-Type: application/json' -d '{"password":"…"}'
curl -sS -b cookies.txt -X POST http://localhost:30303/api/bundle/export \
  -H 'Content-Type: application/json' \
  -d '{"passphrase":"sua-passphrase-forte","include_global":true}' \
  -o backup.impamind

# import na VPS
curl -sS -c vps.txt -X POST https://memoria.impa365.cloud/auth/login \
  -H 'Content-Type: application/json' -d '{"password":"…"}'
curl -sS -b vps.txt -X POST https://memoria.impa365.cloud/api/bundle/import \
  -F "file=@backup.impamind" -F "passphrase=sua-passphrase-forte" -F "overwrite_vault=1"
```

## 6. Atualizar

1. Build/push de uma nova tag de imagem  
2. Altere `IMAGE` na stack (ou mantenha `:1` e faça pull forçado)  
3. Update da stack no Portainer  

O volume `impamind_data` guarda o banco e backups — **não apague** ao atualizar.

## Problemas comuns

| Sintoma | Causa provável |
|---|---|
| 404 no Traefik | `DOMAIN` ou `TRAEFIK_NETWORK` errados; serviço fora da rede do Traefik |
| Certificado não emite | DNS ainda não aponta / `CERT_RESOLVER` com nome diferente |
| Cookie de login some | `IMPAMIND_PUBLIC_URL` sem `https://` ou `COOKIE_SECURE` inconsistente |
| Consegue abrir `IP:30303` | Alguém publicou a porta — remova `ports` e feche no firewall |
| Duas réplicas / DB corrompido | `replicas` ≠ 1 — volte para 1 |
