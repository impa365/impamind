# Memória persistente

Comece com memory_recall. Use o session_id em observe, decide, checkpoint e handoff.

Decisões precisam de rationale. O que substituir uma decisão vigente passa por memory_supersede. Não grave segredos em memórias: use o cofre (`memory_vault_put` / `memory_vault_get` / `memory_vault_list`) e referencie `vault:<scope>/<key>`. Escrita global nasce como proposta.
