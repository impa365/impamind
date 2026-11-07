# Deploy em produção — Portainer / Docker Swarm + Traefik

Este guia sobe o ImpaMind com HTTPS em um domínio próprio, atrás do Traefik.
A porta `30303` **nunca** é publicada na VPS: só o Traefik (80/443) fala com a internet.

> **Uma réplica só.** O banco é SQLite; duas réplicas no mesmo volume corrompem os dados.

## Pré-requisitos

1. Docker Swarm ativo (`docker swarm init`) e Portainer instalado.
2. Traefik v2 ou v3 rodando no Swarm com:
   - entrypoints `web` (`:80`) e `websecure` (`:443`);
   - um certificate resolver (ex.: `letsencrypt`);
   - uma rede overlay compartilhada (ex.: `traefik_public`).
3. DNS do domínio (registro A/AAAA) apontando para a VPS.
4. Firewall liberando só `22`, `80` e `443`.

## 1. Gerar os segredos

```bash
openssl rand -hex 32   # IMPAMIND_SECRET
openssl rand -hex 32   # IMPAMIND_BOOTSTRAP_TOKEN
openssl rand -hex 16   # IMPAMIND_ADMIN_PASSWORD (ou uma senha forte sua)
```

Guarde-os em um gerenciador de senhas. **Não troque o `IMPAMIND_SECRET` depois** de
gravar credenciais no cofre: ele é a chave de criptografia do cofre.

## 2. Criar a stack

1. Portainer → **Stacks** → **Add stack** → nome `impamind`.
2. Cole o conteúdo de [`stack.yml`](stack.yml) no Web editor.
3. Em **Environment variables** → **Advanced mode**, cole [`stack.env.example`](stack.env.example)
   e preencha os valores.
4. **Deploy the stack** e aguarde o healthcheck ficar verde.

| Variável | Exemplo | Descrição |
|---|---|---|
| `DOMAIN` | `memory.example.com` | Host público, sem `https://` |
| `IMAGE` | `impa365/impamind:latest` | Imagem a usar (fixe uma tag em produção) |
| `TRAEFIK_NETWORK` | `traefik_public` | Rede overlay do Traefik |
| `TRAEFIK_ENTRYPOINT` | `websecure` | Entrypoint HTTPS |
| `TRAEFIK_HTTP_ENTRYPOINT` | `web` | Entrypoint HTTP (redireciona para HTTPS) |
| `CERT_RESOLVER` | `letsencrypt` | Nome do certResolver do Traefik |
| `IMPAMIND_ADMIN_PASSWORD` | — | Senha da interface web (mín. 10 caracteres) |
| `IMPAMIND_BOOTSTRAP_TOKEN` | — | Primeiro token de agente (mín. 24 caracteres) |
| `IMPAMIND_SECRET` | — | Assina sessões e cifra o cofre |

## 3. Conferir

```bash
# Na VPS: a porta 30303 NÃO pode aparecer em 0.0.0.0
ss -lntp | grep 30303 || echo "ok: porta não publicada"

# De fora
curl -fsS https://memory.example.com/health
```

Abra `https://DOMAIN/` (login com a senha de admin) e `https://DOMAIN/conectar` para
configurar os clientes MCP.

## 4. Atualizar

1. Troque `IMAGE` para a nova tag (ou mantenha `latest` e marque **Re-pull image**).
2. **Update the stack**.

O volume `impamind_data` guarda o banco e os backups automáticos — não o apague.

## 5. Migrar dados entre instâncias (`.impamind`)

Formato descrito em [`docs/impamind-format.md`](../docs/impamind-format.md).

1. Na origem: **Admin → Clone .impamind** → passphrase forte → **Exportar**.
2. No destino: **Admin → Importar .impamind** com a mesma passphrase.

O arquivo contém o cofre (cifrado só com a passphrase). Não versione e apague após a migração.

## Problemas comuns

| Sintoma | Causa provável |
|---|---|
| 404 do Traefik | `DOMAIN` ou `TRAEFIK_NETWORK` errados; serviço fora da rede do Traefik |
| Certificado não emite | DNS ainda não propagou ou `CERT_RESOLVER` com outro nome |
| Login não persiste | `IMPAMIND_PUBLIC_URL` sem `https://` ou cookie seguro inconsistente |
| `IP:30303` abre de fora | Alguém publicou a porta: remova `ports:` e feche no firewall |
| Banco corrompido | Mais de uma réplica: volte `replicas` para `1` |
