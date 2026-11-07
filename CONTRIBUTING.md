# Contribuindo com o ImpaMind

Obrigado pelo interesse! Issues e pull requests são bem-vindos.

## Antes de começar

- Para mudanças grandes, abra uma issue para alinharmos a proposta.
- Vulnerabilidades seguem o [SECURITY.md](SECURITY.md), nunca uma issue pública.

## Ambiente

Requer Go 1.27+ (ou Docker).

```bash
go vet ./...
go test ./...
```

Sem Go instalado:

```bash
docker run --rm -v "$PWD:/src" -w /src -e CGO_ENABLED=0 golang:1.27.2-alpine go test ./...
```

## Regras do código

- **Toda query SQL declara seu escopo.** Use `scoped(...)` em `internal/store`; queries sem
  escopo precisam de um motivo de sistema registrado. O servidor não inicia se isso falhar.
- **Isolamento é testado.** Mudanças em permissões vêm com testes em `*_isolation_test.go`
  ou `internal/auth`.
- **Agentes não aprovam.** Nada pode permitir que um token de agente aprove memórias.
- **Nada de segredos em memórias, logs ou testes.** Use o cofre.
- Rode `gofmt` e mantenha o estilo do código ao redor.

## Pull requests

1. Faça um fork e crie uma branch a partir de `main`.
2. Mantenha o PR focado em um assunto, com testes.
3. Descreva o quê e o porquê da mudança.

Ao contribuir, você concorda que sua contribuição será licenciada sob a
[Apache License 2.0](LICENSE).
