# Benchmark: Mira contra Serena, aider e Probe

Mede, em repositórios reais clonados em commits fixos, duas coisas que não se
confundem:

1. **Qualidade e custo das ferramentas**, sem modelo no meio (suíte estática).
   Cada ferramenta responde às mesmas perguntas sobre os mesmos símbolos, e as
   respostas são comparadas com um gabarito gerado pelo compilador.
2. **Efeito num agente** (suíte de agentes). Subagentes resolvem tarefas cuja
   resposta certa também sai do gabarito, cada um com uma ferramenta, e cada
   chamada é registrada com bytes, latência e código de saída.

A primeira é determinística e barata. A segunda é ruidosa e cara, e só vale
com repetições.

## Ferramentas

| ferramenta | versão | acesso no benchmark |
|---|---|---|
| Mira | este repositório | CLI; também MCP pela ponte |
| Serena | 1.7.0, contexto `agent` | MCP pela ponte `mcpcall` (servidor vivo durante a rodada) |
| Probe | 0.6.0-rc339 | CLI (`search`, `extract`, `query`, `symbols`) |
| aider | 0.86.2 | só o repo map (`--show-repo-map --map-tokens 4096`); o agente do aider exige `ANTHROPIC_API_KEY` |
| grep | ripgrep embutido no Claude Code | linha de base |

`mcpcall` (em `cmd/mcpcall`) mantém um servidor MCP de stdio vivo atrás de um
socket e expõe cada tool como um comando: subagentes, que só têm Bash, recebem
exatamente o texto que um cliente MCP receberia.

## Repositórios

`repos.json` lista os projetos; `repos.lock.json` fixa o commit de cada um.

| repositório | linguagem | papel |
|---|---|---|
| gin | Go | router HTTP; gabarito de go/types |
| flask | Python | gabarito do jedi, incompleto em código dinâmico |
| react-hook-form | TypeScript | mesmo commit das rodadas 1 a 9 do Mira |
| excalidraw | TypeScript | app React, monorepo |
| spring-petclinic | Java | mesmo commit das rodadas anteriores; gabarito do javac |
| react | JavaScript com Flow | só escala e indexação: nenhum type checker serve de gabarito |

## Gabaritos

Cada gerador grava `defs.jsonl` e `refs.jsonl` com o mesmo formato:

| linguagem | gerador | fonte da verdade |
|---|---|---|
| Go | `groundtruth/gotruth` | go/types, com testes |
| TypeScript | `groundtruth/tstruth/refs.mjs` | type checker do TypeScript 6 (o 7 não expõe mais a API JavaScript) |
| Python | `groundtruth/pytruth/refs.py` | jedi 0.20 |
| Java | `groundtruth/javatruth/JavaTruth.java` | javac (com.sun.source), sem o classpath das dependências |

Em Go, TypeScript e Java o gabarito é completo: nome na linha sem referência
resolvida é comentário, string ou outro símbolo, e conta como falso positivo.
Em Python o jedi não resolve usos dinâmicos (fixtures do pytest, por exemplo);
essas linhas viram `unknown` e ficam fora da precision.

## Suíte estática

- `static/sample.py` sorteia 40 símbolos por repositório, estratificados por
  tipo (funções e métodos, tipos, membros, valores), metade com homônimos, com
  2 a 200 referências. A semente é fixa. Com `--set` e `--scale`, sorteia outra
  amostra, maior, sem mexer na publicada.
- `static/run.py` faz cada ferramenta responder a duas perguntas por símbolo:
  onde está a definição e quais são as referências.
- `static/score.py` pontua contra o gabarito e grava `results/static-<data>.md`.
- `static/misses.py` agrupa o que uma ferramenta perdeu ou inventou por padrão
  de código: é a análise de erro que diz o que corrigir.
- `static/recall.py` mede o recall do Mira numa amostra (em geral a ampliada),
  com intervalo de 95%, e diz por que cada referência se perdeu consultando o
  índice do próprio Mira: não extraída, ambígua, externa, outra sobrecarga ou
  sem resolução, e, para receiver sem tipo, de onde ele veio. Com qualquer
  referência errada ele sai com código 1: é o portão de precisão.

Métricas:

| métrica | definição |
|---|---|
| precision | referências certas sobre as devolvidas, por linha |
| recall | referências do gabarito que a ferramenta devolveu, por linha |
| homônimos | as mesmas medidas só nos símbolos cujo nome aparece em outra definição |
| sem gabarito | usos que o gabarito não decide (o jedi não resolveu, ou o arquivo ficou fora do gabarito por build tag); não entram na precision |
| acerto no 1º | o primeiro candidato de definição contém a linha certa |
| bytes | tamanho da saída no formato padrão da ferramenta, o que um agente leria |
| ms | latência da chamada, com o servidor ou índice já aquecido |

O Mira aparece de dois jeitos. `mira` usa
`resolve <Container.nome> --include-refs`, as referências resolvidas para aquela
definição. `mira-refs` usa `refs <nome>`, o que um agente digita sem
qualificar, com os homônimos misturados. Nos dois casos o adaptador pede a
lista inteira, porque por padrão o Mira corta em 50 e 200 com uma dica.

## Suíte de agentes

- `agent/make_tasks.py` gera tarefas do gabarito. **impact**: listar os usos
  de produção de um símbolo com homônimos. **rename**: renomear um símbolo sem
  tocar nos homônimos; símbolos com homônimo em interface ficam de fora.
- `agent/prepare.py <tarefa> <braço>` cria a rodada: cópia do repositório nas
  edições, atalhos das ferramentas, o comando `b` que registra cada chamada e o
  prompt com o guia da ferramenta (a skill no Mira, as instruções da
  própria Serena, o repo map no aider).
- O prompt vai para um subagente (Haiku ou Sonnet).
- `agent/grade.py <rodada> --tokens N --tool-uses N --duration-ms N` corrige e
  junta o custo. Impacto: precision e recall das linhas citadas. Rename: linhas
  que mudaram, homônimos intactos, linhas alteradas fora da chave e, em Go,
  `go build` e `go vet` na cópia.

Braços: `baseline` (rg, find, sed, cat), `mira`, `serena`, `probe` e
`aider-map` (o mapa no prompt, sem busca). Todos leem arquivos com sed e cat.

## Como rodar

```bash
bash benchmark/scripts/setup.sh            # WITH_JDK17=1 para a Serena importar o petclinic
source benchmark/scripts/env.sh
cd benchmark
_tools/bin/gotruth -root _repos/gin -out _data/groundtruth/gin
node groundtruth/tstruth/refs.mjs _repos/react-hook-form _data/groundtruth/react-hook-form
_tools/py312/bin/python groundtruth/pytruth/refs.py _repos/flask _data/groundtruth/flask
java groundtruth/javatruth/JavaTruth.java _repos/spring-petclinic _data/groundtruth/spring-petclinic
cd static && python3 sample.py gin && python3 run.py gin && python3 score.py gin
python3 sample.py --set recall --scale 8 gin && python3 run.py gin --set recall --tools mira && python3 recall.py --set recall gin
```

`_repos`, `_tools` e `_data` são gerados e regeneráveis; o prefixo `_` os tira
do `go ./...`.

## Decisões de justiça

- A Serena roda no contexto `agent`, o conjunto mais completo de tools.
- No petclinic, o import Gradle da Serena exige um JDK 17 que a máquina não
  tinha; sem ele o servidor Java não acha nenhum símbolo. O benchmark instala
  um JDK 17 isolado e aponta `gradle_java_home` para ele.
- O Probe roda com `--allow-tests`, porque o gabarito inclui testes.
- Referências da Serena custam duas chamadas (`find_symbol` para achar o
  `name_path` exato e `find_referencing_symbols`), como para um agente.
- Latências medem cada ferramenta aquecida; o custo de subir o índice ou o
  language server é medido à parte.

## Limites conhecidos

- Os subagentes chamam as ferramentas por Bash, então o custo de schema MCP por
  turno não entra nos tokens; ele é medido à parte com `mcpcall list`.
- Rodadas com `claude -p` e MCP nativo exigem `claude login` no terminal.
- O aider como agente precisa de chave de API; sem ela entra só o repo map.
- O gabarito de Python é incompleto, e o de Java não enxerga membros herdados de
  dependências ausentes.
- O gabarito de Go usa as build tags padrão: arquivos com outras tags
  (`binding_nomsgpack.go` no gin) ficam fora dele, e usos ali contam como sem
  gabarito, não como erro.

## Resultados da primeira rodada (2026-09-14)

Tabelas completas em `results/static-2026-09-14.md` e `results/agent-2026-09-14.md`.

### Suíte estática

Referências, precision / recall em porcentagem (Python: jedi, com `unknown` fora da precision):

| repositório | Mira | Serena | Mira `refs <nome>` | grep | Probe |
|---|---|---|---|---|---|
| gin (Go) | 92 / 94 | 98 / 99 | 27 / 99 | 16 / 100 | 9 / 100 |
| flask (Python) | 100 / 73 | 99 / 98 | 56 / 93 | 37 / 100 | 7 / 91 |
| spring-petclinic (Java) | 100 / 84 | 90 / 100 | 69 / 86 | 23 / 100 | 22 / 100 |
| excalidraw (TypeScript) | 100 / 65 | 78 / 80 | 5 / 96 | 2 / 100 | 2 / 100 |
| react-hook-form (TypeScript) | 100 / 32 | 93 / 27 | 8 / 84 | 3 / 98 | 2 / 99 |

Definição, acerto no primeiro candidato:

| repositório | Mira | Serena | grep | Probe | repo map do aider |
|---|---|---|---|---|---|
| gin | 82% | 65% | 20% | 38% | 20% |
| flask | 70% | 72% | 20% | 38% | 10% |
| spring-petclinic | 90% | 90% | 42% | 48% | 29% |
| excalidraw | 68% | 72% | 35% | 20% | 0% |
| react-hook-form | 38% | 40% | 25% | 15% | 18% |

Custo por resposta de referências, medianas nos cinco repositórios:

| ferramenta | latência | bytes |
|---|---|---|
| Mira | 10 a 11 ms | 1,0 a 1,9 KB |
| grep | 9 a 22 ms | 1,1 a 6,8 KB |
| Serena | 240 a 340 ms | 1,0 a 1,7 KB |
| Probe | 163 ms a 2 s | 4 a 47 KB |

Custo fixo:

| medida | Mira | Serena | Probe | aider |
|---|---|---|---|---|
| schema MCP por turno | 4,0 KB (tool única), 12,5 KB (todas) | 25,7 KB (`claude-code`), 37,3 KB (`agent`) | 4,1 KB | não é MCP |
| preparo no react (6.743 arquivos) | índice de 44 s, 230 MB | 2,7 s para subir, 1 a 1,7 s por consulta | nenhum, 337 ms por consulta | repo map em 54 s |
| primeira consulta em Java (petclinic) | índice de 0,3 s | 62 s, com o import Gradle | nenhum | repo map em 3 s |

### O que a rodada encontrou

- **Bug do Mira, corrigido**: seguir re-exports de Python recarregava o
  contexto inteiro de cada arquivo. Indexar o flask levava 89 s e 10 GB, com
  0 arestas; passou para 1,2 s e 53 MB.
- **Lacunas do Mira, corrigidas depois da rodada**: membros de
  `type X = { … }` em TypeScript não viravam símbolos, o que derrubava o recall
  no react-hook-form e no excalidraw (a Serena tem a mesma falha em parte
  deles). Em Go, o tipo de variável não tinha escopo, e um receptor não
  analisado caía na busca por nome (os falsos positivos do gin). Os números
  depois da correção estão em "Segunda rodada".
- **Lacuna aberta**: em Python, cadeias por proxy dinâmico
  (`current_app.json.dumps`) ficam de fora.
- **Atrito do Mira com agentes, corrigido depois da rodada**:
  `refs Container.member` devolvia zero, e a skill citava `include_refs` e
  `exclude_tests`, nomes de parâmetro MCP que a CLI não aceitava. Em três das
  quatro tarefas de impacto o agente gastou duas a três chamadas antes da certa.
- **Serena**: sem um JDK 17 o import Gradle do petclinic falha e nenhum símbolo
  aparece; no contexto `agent` as instruções mandam chamar uma tool que o
  contexto não tem.
- **Probe**: recall alto, precision de 2% a 37%, e respostas que chegam a
  580 KB no p90 do react-hook-form.
- **Repo map do aider**: com 4.096 tokens mostra de 0% a 29% das definições
  sorteadas e nada sobre referências.

### Piloto com agentes

Haiku, uma repetição por célula: quatro tarefas geradas do gabarito, cinco
braços, 20 rodadas. Os tokens incluem o prompt de cada braço; a skill do
Mira e o manual da Serena têm cerca de 10 KB, e o repo map de 11 a 14 KB.
Tabela completa em `results/agent-2026-09-14.md`.

| tarefa | braço | resultado | chamadas | lido | tokens | tempo |
|---|---|---|---|---|---|---|
| gin: renomear `Context.Param` (23 linhas, 123 usos de homônimos) | Mira | completo, compila | 3 | 50 KB | 44 mil | 22 s |
| | Serena | completo, compila, 3 linhas a mais | 6 | 3 KB | 46 mil | 45 s |
| | baseline | completo, compila, 5 linhas a mais | 40 | 45 KB | 57 mil | 135 s |
| | aider map | completo, compila, 2 linhas a mais | 30 | 363 KB | 73 mil | 164 s |
| | Probe | completo, compila, 6 linhas a mais | 142 | 2,2 MB | 93 mil | 461 s |
| petclinic: usos de `Owner.getPet(Integer)` | Mira | 4 de 4 | 4 | 4,9 KB | 47 mil | 35 s |
| | baseline | 4 de 4 | 7 | 12 KB | 47 mil | 39 s |
| | Serena | 4 de 4 | 9 | 4,0 KB | 49 mil | 73 s |
| | aider map | 4 de 4 | 31 | 46 KB | 71 mil | 130 s |
| | Probe | 4 de 4 | 51 | 315 KB | 88 mil | 194 s |
| excalidraw: usos de `StoreChange.create` | baseline | 4 de 4 | 4 | 3,3 KB | 42 mil | 25 s |
| | Mira | 4 de 4 | 4 | 2,8 KB | 45 mil | 43 s |
| | Probe | 4 de 4 | 3 | 712 KB | 43 mil | 62 s |
| | Serena | 0 de 4; 4 de 4 com ±1 linha | 1 | 1,1 KB | 44 mil | 25 s |
| | aider map | 4 de 4 | 117 | 1,3 MB | 83 mil | 261 s |
| flask: usos de `JSONProvider.dumps` | Mira | 3 de 4, nenhum errado | 3 | 1,1 KB | 44 mil | 22 s |
| | Serena | 0 de 4; 4 de 4 e 1 a mais com ±1 linha | 1 | 1,3 KB | 44 mil | 27 s |
| | Probe | 4 de 4 e 2 a mais | 28 | 207 KB | 72 mil | 122 s |
| | baseline | 4 de 4 e 3 a mais | 32 | 22 KB | 59 mil | 155 s |
| | aider map | 2 de 4 e 2 errados | 67 | 786 KB | 71 mil | 225 s |

Leitura:

- Todos os braços completaram o rename do gin sem tocar em homônimo. A
  diferença foi o caminho: de 3 a 142 chamadas e de 22 s a 461 s.
- Mira e Serena foram os mais baratos em chamadas e tempo. O baseline
  empata quando a chamada é qualificada e o grep acha direto (excalidraw).
- A Serena achou os lugares certos com uma chamada, mas em duas tarefas o
  agente copiou a numeração que começa em zero. É uma armadilha de interface,
  não de busca; no petclinic o agente conferiu cada linha com `sed` e acertou.
- Sem busca (aider map) ou com busca de alto recall e baixa precision (Probe),
  o agente lê de centenas de KB a MB.
- Os tokens variam bem menos que as chamadas, porque o custo fixo de cada
  turno domina.

### Limites desta rodada

- Uma repetição por célula e só Haiku: serve para validar o harness e apontar
  tendências, não para afirmar diferença pequena.
- As tarefas de impacto com chamada estática (`StoreChange.create`) são fáceis
  para grep; a próxima rodada deve preferir métodos chamados por variável.
- Parte das latências do excalidraw foi medida com subagentes leves rodando ao
  mesmo tempo; a ordem de grandeza entre as ferramentas não muda.

## Segunda rodada: depois das correções (2026-09-14)

Correções no Mira entre as rodadas:

- Membros de `type X = { … }` viram símbolos, inclusive dentro de `A & {…}`,
  unions, `Readonly<{…}>` e objetos aninhados (`customData.generationData`). Os
  tipos ao lado do objeto são bases do alias, e um membro declarado nos dois
  lados de uma union fica ambíguo.
- Tipos de variável com escopo em Go (blocos, `if`, `switch v := x.(type)`,
  `range`, `select`) e em TypeScript (função, bloco, laço). `(T{}).M()` usa o
  tipo do literal, `super.m()` parte da classe estendida, e `Readonly<T>`,
  `Pick<T, …>` e `T | null` levam aos membros de T.
- Fora do Java, uma chamada cujo receptor não foi entendido fica `unresolved`,
  sem cair na busca por nome.
- `refs Container.member` lista só os usos resolvidos para aquela definição, e
  `Container.member:linha` escolhe a sobrecarga. As flags da CLI têm os nomes
  dos parâmetros MCP (`--exclude-tests`, `--include-refs`), com os nomes antigos
  aceitos.

### Suíte estática

Referências do Mira (`resolve --include-refs`), precision / recall:

| repositório | primeira rodada | segunda rodada |
|---|---|---|
| gin | 92 / 94 | 100 / 95 |
| flask | 100 / 73 | 100 / 73 |
| spring-petclinic | 100 / 84 | 100 / 84 |
| excalidraw | 100 / 65 | 100 / 74 |
| react-hook-form | 100 / 32 | 100 / 36 |

Definição, acerto no primeiro candidato / em algum: excalidraw de 68% / 75% para
75% / 85%, react-hook-form de 38% / 45% para 48% / 57%. Os outros não mudaram.

Novo braço `mira-qrefs`: `refs <Container.nome>`, o que o agente digita ao
qualificar. Em membros ele fica só com os usos resolvidos; nome de topo sem
ponto responde igual a `refs <nome>`.

| repositório | `refs <nome>` | `refs <Container.nome>` |
|---|---|---|
| gin | 27 / 99 | 58 / 96 |
| flask | 56 / 93 | 57 / 86 |
| spring-petclinic | 69 / 86 | 76 / 84 |
| excalidraw | 5 / 96 | 12 / 93 |
| react-hook-form | 8 / 84 | 8 / 84 |

Referências no índice inteiro, binário da primeira rodada contra o atual, os dois
com índice novo:

| repositório | resolvidas | ambíguas | sem resolução |
|---|---|---|---|
| gin | 8.035 → 8.143 | 233 → 184 | 1.119 → 1.127 |
| excalidraw | 48.913 → 52.532 | 132 → 405 | 63.983 → 62.282 |
| react-hook-form | 8.144 → 8.375 | 823 → 823 | 19.174 → 18.975 |

flask e petclinic ficaram iguais. No excalidraw, o escopo de variáveis em
TypeScript tirou cerca de 1.900 resoluções que pegavam o tipo de outra variável
com o mesmo nome em outra função (`element`, `appState`, `app`). A amostra do
gabarito não perdeu recall com isso, e pelo menos um desses tipos emprestados
estava errado (`typeChecks.ts`). As ambíguas sobem com membros declarados em
vários lados de uma union.

### Agentes

Haiku, mesmo prompt com a skill atualizada; só o braço `mira` foi repetido.

| tarefa | primeira rodada | segunda rodada |
|---|---|---|
| excalidraw: usos de `StoreChange.create` | 4 chamadas, 2,8 KB, 43 s | 1 chamada, 0,7 KB, 22 s |
| flask: usos de `JSONProvider.dumps` | 3 chamadas, 1,1 KB, 22 s | 1 chamada, 0,6 KB, 14 s |
| petclinic: usos de `Owner.getPet(Integer)` | 4 chamadas, 4,9 KB, 35 s | 1 chamada, 0,8 KB, 16 s |
| gin: renomear `Context.Param` | 3 chamadas, completo, compila | 6 chamadas, completo, compila |

A qualidade não mudou: 4 de 4 no excalidraw e no petclinic, 3 de 4 sem erro no
flask, rename sem linha a mais. No petclinic, uma rodada anterior ao `:linha` em
`refs` gastou 3 chamadas e 9 KB para isolar a sobrecarga. No gin, as chamadas a
mais foram conferências com `snippet` depois do rename.

### O que ainda falta

- TypeScript: membros de objetos literais em parâmetros (`opts: { type: … }`),
  declarações locais dentro de testes, assinaturas de sobrecarga (`useWatch`,
  `insert`), estreitamento por type guard (`isArrowElement(el) && el.elbowed`) e
  tipo inferido em callback, `for … of` e desestruturação.

### Reindexação e edição

Medido numa cópia do excalidraw (813 arquivos, 122 mil refs; o `App.tsx` tem 12
mil linhas):

| cenário | no começo | depois |
|---|---|---|
| `index --full` sobre um índice existente | 3 min 6 s | 9,0 s |
| índice novo | 24 s | 8,8 s |
| consulta com o índice em dia (refresh sem mudança) | 9,9 s | 0,015 s |
| edição no corpo do `App.tsx` | 17,9 s | 1,2 s |
| edição no corpo de um arquivo pequeno | 10,1 s | 0,055 s |
| arquivo ganha uma definição exportada | 10,1 s | 0,055 s |
| assinatura de `t`, usada por 95 arquivos | – | 1,0 s |

Por que demorava e o que mudou:

- Apagar um arquivo varria `refs` e `imports` inteiros para cada símbolo:
  `refs.container_symbol_id` e `imports.resolved_symbol_id` têm `ON DELETE SET
  NULL` e não tinham índice. A migração v6 cria os dois.
- O resolvedor fazia milhares de consultas pequenas (os exports de um barril, os
  membros de um tipo), e cada uma pagava um lock do WAL: eram três quartos do
  tempo de resolver o `App.tsx`. As respostas agora ficam guardadas durante a
  rodada, e a gravação da resolução usa statements preparados.
- Uma edição regravava o arquivo com ids novos, e todo arquivo que usava um
  símbolo dele era resolvido de novo. Agora os símbolos que continuam no arquivo
  mantêm o id, e só quem usava um símbolo que mudou de assinatura ou sumiu é
  resolvido de novo.
- O refresh re-resolvia sempre os arquivos com import pendente ou ref ambígua.
  Agora não resolve nada quando nada mudou e volta só aos imports pendentes e às
  refs ambíguas com um nome que apareceu, sumiu ou mudou. Um `tsconfig*.json`
  alterado resolve tudo, porque `paths` e `baseUrl` mudam o destino dos imports.
- Os 103 imports que nunca resolviam vinham de formas que o extrator ignorava:
  `export const { useAtom } = jotai`, `export { atom }` de um nome importado,
  `export default React.memo(Canvas)` e `export declare class`. Hoje são 0.
- `--full` constrói o índice num banco à parte e troca o conteúdo do banco em uso
  de uma vez, pela API de backup do SQLite. Quem está lendo, como um servidor MCP,
  vê o índice antigo até o fim e o novo depois, nunca vazio ou pela metade.
  Apagar e recriar o arquivo deixaria esse servidor lendo o arquivo apagado.
- Inserir várias linhas por comando foi medido e ficou mais lento neste driver
  (0,85 s contra 0,74 s na edição do `App.tsx`), então ficou uma linha por comando.
- O que resta na edição do `App.tsx` é regravar 52 mil palavras e 9,5 mil refs
  (0,75 s) e resolver o próprio arquivo (0,43 s).

## Terceira rodada: recall numa amostra maior (2026-09-14)

Com 40 símbolos por repositório, o recall tem margem larga demais para orientar
correções. `static/recall.py` mede o Mira numa amostra ampliada
(`sample.py --set recall --scale 8`), com intervalo de 95% por bootstrap, e
classifica cada referência perdida pelo índice do próprio Mira. O relatório, com
exemplos de cada causa, está em [results/recall-2026-09-14.md](results/recall-2026-09-14.md).

| repositório | símbolos | precision | recall | IC 95% | recall com 40 símbolos |
|---|---|---|---|---|---|
| gin | 320 | 100% | 86% | 80–91% | 95% |
| flask | 292 | 100% | 68% | 62–74% | 73% |
| spring-petclinic | 107 | 100% | 68% | 58–78% | 84% |
| excalidraw | 320 | 100% | 70% | 61–79% | 74% |
| react-hook-form | 239 | 100% | 68% | 55–77% | 36% |

Com 40 símbolos, o intervalo do react-hook-form ia de 16% a 64%: os 36% eram
ruído da amostra, não o recall do Mira nesse repositório.

O portão de precisão falhou na primeira passada. As falhas eram de três tipos,
todas corrigidas antes dos números acima:

- Harness: um uso num arquivo fora do gabarito (build tag) contava como erro e
  agora conta como sem gabarito. No Python, uma linha que declara um homônimo e
  também usa o nome (`class Flask(flask.Flask):`) deixou de contar como erro.
- Gabarito de Java: method references (`NamedEntity::getName`) não eram
  registradas. O petclinic ganhou 2 referências.
- Mira, em TypeScript: uma variável ou um parâmetro local não escondia o símbolo
  de topo de mesmo nome (`let i = 0` fora e `(field, i) => i` dentro), e o
  default de um parâmetro desestruturado (`{ onSubmit = noop }`) virava
  declaração. Eram 47 referências erradas no excalidraw e no react-hook-form;
  comparando os binários na mesma amostra, nenhuma referência certa se perdeu.

Causas das referências perdidas, somando os cinco repositórios (os rótulos são os
do relatório):

| causa | perdidas | parte |
|---|---|---|
| unresolved, receiver without type | 945 | 31% |
| not extracted | 850 | 28% |
| unresolved, bare name | 567 | 19% |
| unresolved, member not found on the receiver type | 286 | 9% |
| resolved to another overload | 212 | 7% |
| ambiguous | 131 | 4% |
| marked external | 33 | 1% |
| resolved to another definition | 16 | 1% |
| unresolved, chain from a function call | 6 | 0% |

Sobrecargas contam como perda porque o Mira e o gabarito escolhem declarações
diferentes do mesmo conjunto: no flask o Mira liga a chamada à primeira
`@overload`, e no TypeScript à implementação; o gabarito faz o contrário.

Cada causa principal tem um exemplo mínimo em `internal/resolve/gaps_test.go`.
Enquanto a lacuna está aberta, o teste só exige que o uso não aponte para a
definição errada; quando o resolvedor passar a achar o alvo, o teste pede para
fechar a lacuna e passa a proteger o ganho. Três exemplos que tentei já resolvem
(composite literal numa tabela de testes, tipo importado sob `TYPE_CHECKING`,
atributo tipado no `__init__`): a causa real dessas perdas no gin e no flask
ainda não foi isolada.

## Quarta rodada: regras sem type checker (2026-09-15)

As causas da terceira rodada viraram regras, uma por padrão medido, cada uma com um
caso em `internal/resolve/gaps_test.go` e mantida só se a precisão continuasse em
100%. Na mesma amostra ampliada, comparando com o binário anterior, nenhuma
referência certa se perdeu e nenhuma errada entrou. Relatório em
[results/recall-2026-09-15.md](results/recall-2026-09-15.md).

| repositório | recall antes | recall depois | IC 95% | certas a mais |
|---|---|---|---|---|
| gin | 86% | 89% | 84–93% | 113 |
| flask | 68% | 77% | 71–83% | 119 |
| spring-petclinic | 68% | 99% | 99–100% | 199 |
| excalidraw | 70% | 77% | 70–85% | 268 |
| react-hook-form | 68% | 75% | 64–83% | 208 |

Regras:

- Java: um campo usado como objeto de chamada, de acesso ou de method reference
  (`owners.findById()`, `this.owners.save()`, `owners::findById`) é referência ao
  campo; campos da classe de fora valem numa classe interna; `Visit::getDate`
  referencia a classe.
- Go: uma definição repetida em arquivos com build tags opostas resolve para a do
  build padrão (`go/build`, `MatchFile`).
- TypeScript: o receiver de uma chamada (`BoundElement.unbind()`), os nomes de
  `export { }` e de `export { } from` e os membros de uma local com tipo declarado
  (`values?.content`) viram referências. Tipos derivados: acesso indexado
  (`App["scene"]`), desestruturação (`const { field } = useController()`) e o
  primeiro parâmetro de callbacks de array (`items.forEach((item) => …)`). Funções
  declaradas em callbacks de `describe` e `it` viram símbolos.
- Python: um import dentro de função vale só nela, e um nome solto resolve para o
  `def` ou `class` aninhado na função que contém o uso antes do topo do módulo.

Causas do que ainda se perde:

| causa | gin | flask | spring-petclinic | excalidraw | react-hook-form | total |
|---|---|---|---|---|---|---|
| unresolved, receiver without type | 66 | 16 | 1 | 450 | 199 | 732 |
| unresolved, bare name | 97 | 7 | 2 | 170 | 139 | 415 |
| unresolved, member not found on the receiver type | 89 | 70 | 1 | 106 | 104 | 370 |
| not extracted | 84 | 130 | 0 | 19 | 96 | 329 |
| resolved to another overload | 0 | 57 | 0 | 46 | 109 | 212 |
| marked external | 29 | 4 | 0 | 0 | 0 | 33 |
| ambiguous | 0 | 7 | 0 | 9 | 7 | 23 |

A maior parte do que sobra pede type checker ou é diferença de identidade:
parâmetros tipados pelo contexto (`perform: (elements, appState, _, app) =>
app.scene`), o global `h` dos testes do excalidraw, utilitários genéricos,
membros herdados de bibliotecas, tipos declarados dentro de funções de teste em Go
e sobrecargas. Duas lacunas continuam abertas no ledger: a variável criada por
`sync.OnceValue` no Go e a função usada como receiver no Python.

## Comparação na amostra ampliada (2026-09-15)

Todas as ferramentas rodaram de novo na amostra ampliada (`run.py <repo> --set recall`,
`score.py --set recall`), com o Mira da quarta rodada. As tabelas completas estão em
[results/static-recall-2026-09-15.md](results/static-recall-2026-09-15.md); a rodada
publicada de 40 símbolos continua em `results/static-2026-09-14.md`.

Referências, precision / recall:

| repositório | símbolos | Mira | Serena | grep | Probe | `refs <nome>` | `refs <Container.nome>` |
|---|---|---|---|---|---|---|---|
| gin | 320 | 100% / 89% | 96% / 96% | 25% / 100% | 11% / 100% | 42% / 98% | 67% / 95% |
| flask | 292 | 100% / 77% | 99% / 94% | 48% / 100% | 7% / 93% | 75% / 90% | 79% / 82% |
| spring-petclinic | 107 | 100% / 99% | 81% / 100% | 26% / 100% | 15% / 100% | 50% / 100% | 94% / 99% |
| excalidraw | 320 | 100% / 77% | 95% / 80% | 6% / 100% | 3% / 100% | 19% / 99% | 23% / 88% |
| react-hook-form | 239 | 100% / 75% | 99% / 38% | 4% / 99% | 3% / 99% | 12% / 96% | 12% / 96% |

Latência mediana por consulta de referências: Mira 10 ms, grep 9–21 ms,
Serena 257–328 ms, Probe 147–696 ms.

Definição, acerto no primeiro candidato / em algum:

| repositório | Mira | Serena | grep | Probe | aider repo map |
|---|---|---|---|---|---|
| gin | 79% / 88% | 62% / 92% | 21% / 100% | 30% / 100% | 12% / 12% |
| flask | 79% / 92% | 80% / 93% | 38% / 100% | 38% / 100% | 12% / 12% |
| spring-petclinic | 97% / 100% | 97% / 100% | 48% / 100% | 49% / 100% | 24% / 24% |
| excalidraw | 73% / 88% | 76% / 91% | 35% / 100% | 19% / 100% | 2% / 2% |
| react-hook-form | 53% / 72% | 44% / 67% | 20% / 100% | 11% / 99% | 5% / 5% |

Com mais símbolos, o Mira continua sem nenhuma referência errada. Em recall ele empata
com o Serena no petclinic, fica perto no excalidraw, passa no react-hook-form e fica
atrás no gin e no flask. A precisão do Serena cai com a amostra maior: 81% no
petclinic e 95% no excalidraw.

Boa parte do que o Mira ainda perde vem de símbolos cuja definição o adaptador não
acha (`definition not found`): o Mira responde a definição que ele conhece, e ela não
cobre a linha sorteada, então nenhuma referência entra.

| repositório | símbolos sem definição achada | referências do gabarito neles | parte das perdidas do Mira |
|---|---|---|---|
| gin | 37 | 271 | 74% |
| flask | 22 | 108 | 36% |
| spring-petclinic | 0 | 0 | 0% |
| excalidraw | 40 | 279 | 34% |
| react-hook-form | 67 | 320 | 49% |

As causas são de identidade, não de resolução: sobrecargas (o gabarito aponta a
primeira assinatura e o Mira guarda a implementação: `useWatch`, `addEventListener`,
`insert`), tipos, campos e variáveis declarados dentro de funções de teste
(`exampleStruct.A` no gin, `FormValues` no react-hook-form) e funções aninhadas com
outro nome qualificado (`Blueprint.extend` no gabarito é
`Blueprint._merge_blueprint_funcs.extend` no Mira). O Serena tem o mesmo tipo de
falha em até 79 símbolos por repositório, nenhum no petclinic (a coluna de erros das tabelas completas).
