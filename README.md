<div align="center">

# 🧠 ImpaMind

**Memória persistente, auditável e compartilhada para agentes de IA.**

Um único servidor que dá ao Cursor, Claude Code, Gemini CLI e qualquer cliente MCP
uma memória de longo prazo por projeto — com decisões versionadas, revisão humana,
cofre de credenciais cifrado e isolamento rígido entre projetos.

[![CI](https://github.com/impa365/impamind/actions/workflows/ci.yml/badge.svg)](https://github.com/impa365/impamind/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)](go.mod)
[![Docker](https://img.shields.io/docker/v/impa365/impamind?label=docker&logo=docker&logoColor=white&sort=semver)](https://hub.docker.com/r/impa365/impamind)
[![MCP](https://img.shields.io/badge/MCP-Streamable%20HTTP-8A2BE2)](https://modelcontextprotocol.io)

[Recursos](#-recursos) •
[Início rápido](#-início-rápido) •
[Conectar agentes](#-conectar-agentes-mcp) •
[Deploy](#-deploy-em-produção) •
[Configuração](#%EF%B8%8F-configuração) •
[Segurança](#-modelo-de-segurança) •
[Licença](#-licença-e-créditos)

</div>

---

## O problema

Todo agente de IA começa cada conversa do zero. Ele esquece a arquitetura que vocês
decidiram ontem, repete erros já resolvidos e não sabe o que outro agente fez no mesmo
projeto. As "memórias" locais de cada ferramenta ficam presas a uma máquina, sem histórico,
sem revisão e misturando projetos.

## A solução

O **ImpaMind** é um servidor central de memória. Os agentes seguem um protocolo simples:

```text
recall  →  trabalhar  →  observe / decide  →  checkpoint  →  handoff
```

- **No início da tarefa**, o agente chama `memory_recall` e recebe um pacote enxuto (com
  orçamento de tokens) com as preferências globais, as decisões vigentes do projeto, as
  pendências, o último checkpoint e as memórias relevantes para a tarefa.
- **Durante o trabalho**, registra aprendizados (`memory_observe`) e decisões com
  justificativa (`memory_decide`). Duplicatas são detectadas e segredos são bloqueados.
- **Ao pausar ou terminar**, salva o estado (`memory_checkpoint`) e passa o bastão
  (`memory_handoff`) para o próximo agente — que pode ser outra ferramenta, em outra máquina.

Tudo fica versionado, com linhagem, evidências e trilha de auditoria.

## ✨ Recursos

| | |
|---|---|
| 🔌 **MCP nativo** | Streamable HTTP em `/mcp` (agentes) e `/mcp/admin` (humanos). Funciona com Cursor, Claude Code, Gemini CLI, VS Code e qualquer cliente MCP. |
| 🗂️ **Projetos isolados** | Cada projeto é um escopo próprio. Tokens recebem permissões por projeto; o isolamento é aplicado em toda consulta SQL. |
| 🧭 **Resolução automática de projeto** | O agente envia o git remote ou a pasta (`project_hint`) e o ImpaMind encontra o projeto por aliases. Qualquer chamada pode trocar de projeto com `project=`. |
| 🧾 **Decisões com linhagem** | Decisões exigem justificativa. Substituir uma decisão cria uma nova versão (`supersede`) — o histórico nunca é apagado. |
| 👀 **Revisão humana** | Políticas `strict`, `balanced` e `open` por projeto. Tipos protegidos e o escopo global nascem como propostas até alguém aprovar. |
| 🔐 **Cofre de credenciais** | Segredos cifrados com AES-256-GCM, acesso auditado. Memórias só referenciam `vault:<escopo>/<chave>`; senhas no texto são bloqueadas. |
| 🔎 **Busca híbrida** | Full-text SQLite FTS5 (unicode61 + porter) e, opcionalmente, busca semântica com qualquer API de embeddings compatível com OpenAI. |
| 🕸️ **Grafo de conhecimento** | Links entre memórias, sugestão automática de relações e visualização em grafo na interface web. |
| 🧑‍💻 **Interface web** | Navegar e editar memórias, revisar propostas, gerenciar projetos, tokens, cofre, sessões e logs. |
| 📦 **Clone portátil `.impamind`** | Exporte/importe uma instância inteira (projetos, memórias, links e cofre cifrado por passphrase com Argon2id). |
| 🔑 **OAuth 2.1 + tokens** | Registro dinâmico de clientes (PKCE) para clientes MCP que suportam OAuth, ou tokens Bearer de agente/humano. |
| 🪶 **Leve** | Um binário Go estático, imagem `scratch` de ~12 MB, SQLite embutido (sem CGO). Backups automáticos com rotação. |

## 🚀 Início rápido

### Docker Compose (recomendado)

```bash
git clone https://github.com/impa365/impamind.git
cd impamind
cp .env.example .env
# edite .env e preencha IMPAMIND_ADMIN_PASSWORD, IMPAMIND_BOOTSTRAP_TOKEN e IMPAMIND_SECRET
docker compose up -d
```

Abra **http://localhost:30303**, entre com a senha de admin e vá em **Conectar** para
gerar a configuração dos seus agentes.

> Gere segredos fortes com `openssl rand -hex 32`.

### Docker puro

```bash
docker run -d --name impamind -p 30303:30303 \
  -e IMPAMIND_ADMIN_PASSWORD='troque-esta-senha' \
  -e IMPAMIND_BOOTSTRAP_TOKEN="$(openssl rand -hex 32)" \
  -e IMPAMIND_SECRET="$(openssl rand -hex 32)" \
  -v impamind_data:/data \
  impa365/impamind:latest
```

### A partir do código

Requer Go 1.27+.

```bash
go build -o impamind ./cmd/impamind
IMPAMIND_DATA_DIR=./data IMPAMIND_ADMIN_PASSWORD='troque-esta-senha' ./impamind serve
```

## 🔌 Conectar agentes (MCP)

A página **`/conectar`** da sua instância gera tudo pronto. Os exemplos abaixo usam o token
na variável de ambiente `IMPAMIND_TOKEN` — nunca coloque o token direto em arquivos versionados.

<details open>
<summary><b>Cursor</b> — <code>~/.cursor/mcp.json</code></summary>

```json
{
  "mcpServers": {
    "impamind": {
      "url": "https://memory.example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${env:IMPAMIND_TOKEN}"
      }
    }
  }
}
```

</details>

<details>
<summary><b>Claude Code</b></summary>

```bash
claude mcp add --transport http impamind https://memory.example.com/mcp \
  --header "Authorization: Bearer $IMPAMIND_TOKEN"
```

</details>

<details>
<summary><b>Qualquer cliente MCP</b></summary>

- Transporte: **Streamable HTTP**
- URL do agente: `https://SEU_DOMINIO/mcp`
- URL administrativa (token humano): `https://SEU_DOMINIO/mcp/admin`
- Autenticação: `Authorization: Bearer <token>` ou OAuth 2.1
  (descoberta em `/.well-known/oauth-protected-resource`)
- Opcional: `X-Impamind-Project: <slug>` fixa um projeto padrão na conexão;
  o argumento `project` de cada ferramenta tem prioridade sobre ele.

</details>

Depois, adicione as instruções do protocolo ao seu agente. Há modelos prontos em
[`integrations/`](integrations/) para Cursor (`cursor.mdc`), Claude (`CLAUDE.md`),
Codex/agents (`AGENTS.md`) e Gemini (`GEMINI.md`).

### Ferramentas MCP

**Endpoint do agente — `/mcp`**

| Ferramenta | Para quê |
|---|---|
| `memory_recall` | Início da tarefa: pacote de contexto + `session_id` + projetos acessíveis |
| `memory_observe` | Registrar um aprendizado durável (com deduplicação) |
| `memory_decide` | Registrar uma decisão com justificativa |
| `memory_supersede` | Substituir uma memória mantendo a linhagem |
| `memory_checkpoint` | Salvar o estado da sessão (arquivos, erros, testes, próximos passos) |
| `memory_handoff` | Encerrar a sessão com resumo para o próximo agente |
| `memory_search` / `memory_read` | Buscar e ler memórias (com histórico) |
| `memory_review` / `memory_review_resolve` | Listar e resolver conflitos, duplicatas e itens vencidos |
| `memory_vault_list` / `get` / `put` / `delete` | Cofre de credenciais |

**Endpoint administrativo — `/mcp/admin`** (somente token humano)

Tudo acima, mais: `impamind_status`, `project_list`, `project_create`, `project_update`,
`memory_list`, `memory_add`, `memory_update`, `memory_approve`, `memory_reject`,
`memory_transition`, `memory_delete`, `memory_promote`, `link_create`, `link_delete`,
`session_list` e `checkpoint_*`.

## 🏭 Deploy em produção

Para VPS com **Portainer + Docker Swarm + Traefik** (HTTPS automático, porta interna não
exposta), use [`deploy/stack.yml`](deploy/stack.yml) com as variáveis de
[`deploy/stack.env.example`](deploy/stack.env.example).

Passo a passo completo: **[deploy/PORTAINER.md](deploy/PORTAINER.md)**.

> ⚠️ Rode **sempre uma única réplica** — o banco é SQLite.

## ⚙️ Configuração

Toda a configuração é feita por variáveis de ambiente. Segredos também aceitam o sufixo
`_FILE` (ex.: `IMPAMIND_SECRET_FILE=/run/secrets/impamind_secret`) para Docker secrets.

| Variável | Padrão | Descrição |
|---|---|---|
| `IMPAMIND_ADDR` | `:30303` | Endereço de escuta |
| `IMPAMIND_DATA_DIR` | `/data` | Pasta do banco e dos backups |
| `IMPAMIND_DB_PATH` | `$DATA_DIR/impamind.db` | Caminho do SQLite |
| `IMPAMIND_PUBLIC_URL` | — | URL pública (OAuth, `/conectar`, cookies) |
| `IMPAMIND_ADMIN_PASSWORD` | — | Senha da interface web (mín. 10). Sem ela, o login web fica desativado |
| `IMPAMIND_BOOTSTRAP_TOKEN` | — | Cria no boot o primeiro token de agente (mín. 24) |
| `IMPAMIND_SECRET` | aleatório | Assina sessões e cifra o cofre. **Obrigatório em produção; não troque depois** |
| `IMPAMIND_COOKIE_SECURE` | auto (`https`) | Cookie `Secure` |
| `IMPAMIND_CORS_ORIGINS` | — | Origens permitidas, separadas por vírgula |
| `IMPAMIND_MCP_VERSIONS` | `2026-07-28,2025-11-25` | Versões do protocolo MCP aceitas |
| `IMPAMIND_SESSION_OBSERVATION_BUDGET` | `15` | Máximo de observações por sessão de trabalho |
| `IMPAMIND_PROPOSAL_TTL` | `30d` | Prazo para propostas pendentes expirarem |
| `IMPAMIND_SESSION_ARCHIVE_AFTER` | `14d` | Arquiva sessões inativas |
| `IMPAMIND_LOG_RETENTION` | `90d` | Retenção dos logs de auditoria |
| `IMPAMIND_BACKUP_DIR` | `$DATA_DIR/backups` | Pasta dos backups automáticos |
| `IMPAMIND_BACKUP_INTERVAL` | `24h` | Intervalo entre backups |
| `IMPAMIND_BACKUP_KEEP` | `7` | Quantos backups manter |
| `IMPAMIND_RAG` | `false` | Ativa busca semântica |
| `IMPAMIND_EMBED_URL` | `https://api.openai.com/v1` | API de embeddings compatível com OpenAI |
| `IMPAMIND_EMBED_KEY` | — | Chave da API (ou `OPENAI_API_KEY`) |
| `IMPAMIND_EMBED_MODEL` | `text-embedding-3-small` | Modelo de embeddings |
| `IMPAMIND_EMBED_DIMENSIONS` | `512` | Dimensões do vetor |
| `IMPAMIND_JOBS_INTERVAL` | `10m` | Intervalo das rotinas de manutenção |
| `IMPAMIND_LOG_LEVEL` | `info` | `info` ou `debug` |

## 🛡️ Modelo de segurança

- **Isolamento por projeto em todas as consultas.** Cada query SQL é registrada com um
  predicado de escopo obrigatório; o servidor se recusa a iniciar se alguma query não tiver.
- **Agentes nunca aprovam.** Tokens de agente não podem ter permissão de aprovação; o
  escopo global e os tipos protegidos só viram vigentes com aprovação humana.
- **Segredos fora das memórias.** Conteúdo que parece credencial é rejeitado e o agente é
  orientado a usar o cofre. Os valores do cofre são cifrados com AES-256-GCM e cada leitura
  é auditada.
- **Sem estado de sessão no transporte.** Toda requisição MCP é autenticada de novo; o
  `Mcp-Session-Id` não autentica nem escolhe projeto.
- **Superfície mínima.** Imagem `scratch`, sem shell, binário estático.

Encontrou uma vulnerabilidade? Veja [SECURITY.md](SECURITY.md).

## 🧩 Conceitos

| Conceito | Descrição |
|---|---|
| **Escopo** | `global` (preferências e padrões entre projetos) ou um **projeto** |
| **Tipos** | `preference`, `pattern`, `knowledge`, `decision`, `requirement`, `business_rule`, `architecture`, `todo`, `observation`, `note`, `checkpoint`, `session_summary` |
| **Tiers** | `T1` núcleo (sempre no recall) · `T2` trabalho · `T3` contexto |
| **Status** | `proposed` → `active` → `superseded` / `deprecated` / `archived` / `expired` |
| **Sessão de trabalho** | Liga recall, escritas, checkpoints e handoff de um agente em uma tarefa |

## 🧰 Cliente CLI

A imagem serve binários do cliente em `/downloads` (Linux e Windows). O cliente lê
`IMPAMIND_URL`, `IMPAMIND_TOKEN` e `IMPAMIND_PROJECT` do ambiente:

```bash
impamind sync pull --dir .impamind   # exporta as memórias do projeto em Markdown
impamind sync push --dir .impamind   # importa as edições locais (vira nova versão ou proposta)
impamind hook <evento>               # integração com hooks de agentes
```

## 🛠️ Desenvolvimento

```bash
go vet ./...
go test ./...
docker build -t impamind:dev .
```

Estrutura:

```text
cmd/impamind        binário (servidor + cliente com a tag "client")
internal/auth       resolução de permissões (ScopeSet)
internal/authn      tokens, cookies e OAuth
internal/store      SQLite, schema e queries com escopo obrigatório
internal/protocol   regras de recall/observe/decide/checkpoint/handoff/review
internal/mcp        servidor MCP (/mcp e /mcp/admin)
internal/api        API REST e roteamento HTTP
internal/oauth      OAuth 2.1 (registro dinâmico + PKCE)
internal/rag        índice semântico opcional
internal/web        interface web (arquivos estáticos embutidos)
deploy/             stack para Portainer/Swarm + guia
integrations/       instruções do protocolo para cada agente
```

Contribuições são bem-vindas — leia o [CONTRIBUTING.md](CONTRIBUTING.md).

## 📄 Licença e créditos

Distribuído sob a **[Apache License 2.0](LICENSE)**.

Você pode usar, estudar, modificar e distribuir o ImpaMind, inclusive comercialmente,
**desde que mantenha os créditos**: toda cópia ou trabalho derivado deve incluir a licença
e o arquivo [NOTICE](NOTICE) com a atribuição original, e indicar os arquivos alterados.

<div align="center">

Criado e mantido por **[Impa365](https://github.com/impa365)** 💜

</div>
