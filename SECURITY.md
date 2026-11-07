# Política de segurança

## Versões suportadas

Correções de segurança são publicadas sobre a versão mais recente (`main` e a última tag
da imagem `impa365/impamind`).

## Como reportar uma vulnerabilidade

**Não abra uma issue pública.** Use o
[reporte privado de vulnerabilidades do GitHub](https://github.com/impa365/impamind/security/advisories/new).

Inclua, se possível:

- descrição do problema e impacto esperado;
- passos para reproduzir (versão, configuração, requisições);
- prova de conceito mínima.

Respondemos em até 5 dias úteis e combinamos com você a data de divulgação depois da correção.

## Boas práticas de operação

- Defina `IMPAMIND_SECRET`, `IMPAMIND_ADMIN_PASSWORD` e `IMPAMIND_BOOTSTRAP_TOKEN` com
  valores longos e aleatórios (`openssl rand -hex 32`).
- Exponha o servidor só por HTTPS (Traefik ou outro proxy) e não publique a porta `30303`.
- Dê a cada agente um token próprio, com permissão só nos projetos de que ele precisa.
- Arquivos `.impamind` contêm o cofre cifrado pela passphrase: trate-os como backup sensível.
- Nunca versione `.env`, bancos `*.db`, backups ou tokens.
