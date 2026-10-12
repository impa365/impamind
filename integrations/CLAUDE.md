# Memória persistente

No início de cada tarefa chame memory_recall. Passe o session_id devolvido nas chamadas seguintes.

Registre aprendizados com memory_observe e decisões com memory_decide, sempre com rationale. Substitua o que deixou de valer com memory_supersede. Faça memory_checkpoint antes de pausar e memory_handoff ao terminar.

Não grave segredos em memórias: use o cofre (`memory_vault_put` / `memory_vault_get`) e referencie `vault:<scope>/<key>`. O que for para o escopo global fica proposto até aprovação humana na interface.
