# Teste de campo com IA — 14/09/2026

> Escrito quando o projeto se chamava **codegraph**; hoje ele é o **Mira** e os comandos são `mira ...`.

Objetivo: observar um agente de IA usando o `codegraph` em repositórios reais,
medir o que ele consome e anotar atritos, antes de decidir a evolução da busca.

## Setup

- Repositórios reais, clone raso: `spring-projects/spring-petclinic` (50 arquivos
  Java) e `react-hook-form/react-hook-form` (407 TS/TSX + 17 JS).
- Agentes: instâncias independentes de Claude (subagentes), sem conhecimento
  prévio dos repositórios. Todo acesso ao código passa por um wrapper que
  registra cada comando e o tamanho da saída em bytes.
- Braços: **codegraph** (só os subcomandos do codegraph com `--json`, que é o
  que um cliente MCP recebe) versus **baseline** (só `find/grep/cat/sed`).
- Tarefas: (1) petclinic, fluxo de criação de visita; (2) petclinic, impacto de
  adicionar um campo em Owner; (3) react-hook-form, rastrear `register` até a
  validação de `required`.
- Por que não `claude -p` headless: o CLI não herda a credencial do app
  desktop. Com `claude login` no terminal o mesmo experimento roda 100%
  headless com contagem exata de tokens.

## Achado 1 — o parser derruba arquivos reais (bloqueador)

| repositório | arquivos | com erro de parse | símbolos |
|---|---|---|---|
| spring-petclinic | 50 | 5 (10%), todos testes | 266 |
| react-hook-form | 424 | 137 (32%); `src/` core: 70 de 248 | 386 |

Causas, isoladas com testes mínimos no gotreesitter v0.6.7:

- **TSX:** qualquer `async (x) => …` passado **diretamente como argumento** mata
  o parser sem recuperação (`no_stacks_alive`). `f(async (x) => {})` morre;
  `const g = async (x) => {}` e `f(async ({a}: T) => {})` funcionam. É o padrão
  de todo teste (`test('x', async () => …)`), `useEffect` e Playwright.
  Aumentar `GOT_GLR_MAX_STACKS` (32 → 512) não muda nada: não é fan-out.
- **Java:** `>>` fechando generics aninhados sem espaço (`Set<List<String>>`)
  e `class A<T extends Comparable<T>>` geram erro (com recuperação parcial).
  `Set<List<String> >` com espaço funciona: o token source Java trata `>>`
  como shift.
- Em `useForm.ts` e `createFormControl.ts` (coração do react-hook-form) o
  índice ficou sem os símbolos principais.

Consequência: em repositórios TS reais, um terço do grafo some. Nenhuma
melhoria de busca compensa isso; é o primeiro item a resolver.

## Achado 2 — tamanho das saídas

Medido no petclinic (bytes; ~4 bytes por token):

| comando | JSON | texto | razão |
|---|---|---|---|
| `search Owner` (68 hits) | 14.416 | 9.716 | 1,5x |
| `refs Owner` | 41.633 | 13.829 | 3,0x |
| `symbols OwnerController.java` | 7.405 | 2.705 | 2,7x |
| `resolve Owner` | 4.036 | 1.560 | 2,6x |
| `resolve OwnerController` | 1.351 | 813 | 1,7x |

- O MCP entrega JSON indentado, com `id`, `qualified_name`, `file` repetidos
  por item. Para um nome comum, `refs` custa ~10 mil tokens numa chamada.
- `search` sem limite por arquivo devolve 68 linhas para `Owner`.

## Achado 3 — bytes e chamadas por braço

Saída consumida pelo agente (bytes devolvidos pelas ferramentas; ~4 bytes por
token), número de comandos e confiança auto-declarada:

| tarefa | braço | comandos | bytes de saída | ~tokens | confiança |
|---|---|---|---|---|---|
| petclinic 1: fluxo de criação de visita | codegraph | 19 | 31.394 | 7,8k | 92% |
| petclinic 1 | baseline | 15 | 28.166 | 7,0k | 95% |
| petclinic 2: adicionar campo em Owner | codegraph | 25 (limite) | 50.263 | 12,6k | 85% Java / 50% resto |
| petclinic 2 | baseline | 20 | 41.599 | 10,4k | 90% |
| react-hook-form 1: register → required | codegraph | 31 | 72.306 | 18,1k | 85% |
| react-hook-form 1 | baseline | 29 | 23.626 | 5,9k | 95% |

Maior saída única por rodada: codegraph 3,9 KB / 11,9 KB / 30,1 KB; baseline
8,1 KB / 15,0 KB / 3,1 KB. Logs brutos, o wrapper e o resumidor estão em
`docs/field-test-2026-09-14/`.

Leitura: **nas três tarefas o baseline grep+cat consumiu menos bytes e chegou a
respostas iguais ou mais completas.** No react-hook-form o codegraph gastou
3x mais que o baseline. Os motivos aparecem nos relatórios:

- O baseline abre com `find`/`ls` e um `grep` amplo por arquivo, que devolve um
  "índice de linhas" de graça (ex.: um único grep em `createFormControl.ts`,
  de 2.300 linhas, deu todas as âncoras). O codegraph não tem esse comando.
- O codegraph obriga a sequência `search → symbols → snippet` (3 chamadas)
  onde o baseline faz `grep -n` + `sed -n` (2), e cada resposta JSON é 1,5x a
  3x maior que o texto equivalente.
- Repositórios pequenos e bem nomeados não têm o problema que o grafo resolve.
  A vantagem do codegraph apareceu só em perguntas de grafo: callers/callees
  de `Owner.addVisit → Pet.addVisit` em 2 chamadas.

## Achado 4 — atritos relatados pelos agentes (codegraph)

Do mais citado para o menos:

1. **Sem listagem de arquivos e diretórios.** Os três agentes quiseram `ls`/
   `tree` logo após `status`; descobrir arquivos dependeu de eles aparecerem
   em hits de `search`.
2. **`refs` não cobre campos.** `refs telephone` devolveu vazio: só registramos
   chamadas, tipos e `new`, não acesso a campo/propriedade. Para "siga este
   campo" a ferramenta falha no primeiro passo.
3. **`search` não indexa literais de string.** `.param("telephone", …)`,
   `hasProperty("telephone", …)`, chaves i18n: invisíveis. Foi o momento em
   que o agente mais quis `grep -rn '"telephone"'`.
4. **Anotações invisíveis nas assinaturas.** `@PostMapping`, `@ModelAttribute`,
   `@OneToMany(cascade=…)`, `@NotBlank` não aparecem em `symbols`/`resolve`;
   em Spring é o dado mais importante. Custou ~5 chamadas extras por tarefa.
5. **Falha de parse silenciosa.** `symbols OwnerControllerTests.java` devolveu
   `[]` sem aviso; o agente ficou sem os nomes dos testes e adivinhou ranges.
6. **`unresolved` versus `external` confusos.** `JpaRepository.save` veio como
   `unresolved`; o agente não distingue "não sei" de "está numa lib".
7. **Só código-fonte.** Templates Thymeleaf, `schema.sql`, `data.sql`,
   `messages.properties` não existem para a ferramenta; metade da tarefa 2
   ficou em "provável".
8. **`search` sem filtro de caminho** e sem agrupar por arquivo (`param`
   trouxe 55 hits; `NotBlank` 11 quando um interessava).
9. **`snippet` exige números de linha**, então nunca é a primeira chamada.
10. **`resolve` de classe** lista como callers só quem chama o construtor e
    `callees: []`; o grafo real só aparece método a método.
11. **Injeção de dependência invisível**: `callers` de `VisitController` vazio;
    a ligação `model.put("owner") → @ModelAttribute` foi inferência do agente.
12. JSON com `\u003c` dificulta ler generics.

O que os agentes elogiaram: `symbols` + `snippet` com ranges exatos; `callers`
e `callees` acertando cadeias de chamada em 2 chamadas; testes aparecendo como
callers como exemplos de uso; `--include-body` e `Container.member`;
`status` com `outdated: 0`; `snippet` além do fim do arquivo tratado sem erro.

## Achado 5 — react-hook-form: o que acontece quando o índice falha

- `resolve register`, `resolve useForm` e `resolve createFormControl` vieram
  vazios; `symbols` de `useForm.ts`, `createFormControl.ts` e
  `validateField.ts` devolveu `[]`. São exatamente os três arquivos da tarefa,
  todos derrubados pelo bug do `async (x) =>` (Achado 1).
- O agente sobreviveu com uma estratégia esperta: `search` por identificadores
  internos raros (`executeBuiltInValidation`, `updateValidAndValue`,
  `_disableForm`) e `snippet` de intervalos adivinhados. Acertou 85%, mas
  gastou 31 chamadas e 72 KB.
- `search register --limit 40` devolveu 40 hits de `app/src/*.tsx` em ordem
  alfabética de caminho, sem chegar em `src/logic`; `search handleSubmit
  --limit 200` devolveu 30 KB e foi desperdiçado. Busca sem filtro de caminho,
  sem separar definição de uso e sem agrupar por arquivo não escala.
- Sem `symbols`, o agente não sabe onde uma função termina e corta blocos no
  meio (dois snippets para `register`, `methods` cortado antes de `register`).

## Recomendações, em ordem, com a evidência de cada uma

1. **Consertar o parser antes de qualquer busca.** 32% dos arquivos TS reais e
   os três arquivos centrais do react-hook-form somem do grafo. Opções:
   (a) patch local do gotreesitter via `replace` no `go.mod` para o
   `async (x) =>` em argumento e o `>>` de generics em Java, com PR upstream;
   (b) fallback: quando `stop == no_stacks_alive`, reparsear por blocos de
   topo (statements separados por linha em branco) e recompor offsets;
   (c) trocar o runtime (tree-sitter com cgo), o que contraria a premissa
   do projeto. É decisão sua; sem isso nenhum número acima melhora.
2. **`search` com filtro de caminho, agrupamento por arquivo e definição
   primeiro.** `--path src/logic`, `--exclude tests`, hits agrupados por
   arquivo com contagem, e linhas de definição (`function X`, `const X =`,
   `class X`) no topo. Resolve o atrito mais caro do react-hook-form (48 KB
   só em `search`).
3. **Indexar literais de string e acesso a campos.** `refs telephone` vazio e
   `.param("telephone")` invisível são o primeiro tropeço em Spring. Registrar
   `field_access`/`property_identifier` como `RefIdentifier` e guardar
   literais de string na tabela `words` com flag `literal`.
4. **`files` / `tree` e `grep` de linha.** Os três agentes quiseram `ls` na
   primeira chamada. Um `files [--path]` com contagem de símbolos e um
   `search --regex` sobre o texto (o grep que o baseline usa como índice de
   linhas) fecham a distância para o baseline sem perder o grafo.
5. **Anotações e decorators na `SymbolInfo`.** `@PostMapping`, `@OneToMany`,
   `@Valid`, `@Injectable`: já são refs `annotation`; basta expô-las por
   símbolo, com os argumentos.
6. **Falha de parse visível.** `symbols`/`resolve` devem devolver
   `parse_error: true` e uma dica ("use search/snippet"); hoje é silêncio.
7. **`unresolved` versus `external` para membros herdados de libs.**
   `JpaRepository.save` deveria ser `external` porque o tipo do receiver
   resolveu para uma interface cujo pai é externo.
8. **Saída compacta por padrão no MCP.** JSON compacto (sem indent), sem `id`
   quando não usado, e `snippet` com opção `--symbol X` para não exigir
   números de linha; a razão JSON/texto de 1,5x a 3x é puro desperdício.
9. **Indexar arquivos não-código como texto.** Templates, SQL, properties
   só na tabela `words` (sem símbolos) já teriam salvado metade da tarefa 2.
10. **Só depois disso, busca semântica.** Os atritos medidos são de
    cobertura, filtro e formato, não de "não sei o nome". O único caso
    "semântico" foi o agente escolher identificadores raros para achar o
    núcleo, e um `search` com definição-primeiro resolve sem embeddings.

## Método: limitações deste teste

- Subagentes no lugar de `claude -p` headless: tokens exatos por rodada não
  foram medidos, só bytes de saída das ferramentas (proxy de ~4 bytes/token).
- Uma rodada por tarefa e braço; variância não medida.
- Repositórios pequenos (50 e 424 arquivos). Em monorepos grandes o `grep`
  amplo do baseline devolve muito mais lixo, e é onde o grafo deveria ganhar.

## Depois das mudanças (mesmo dia)

Aplicados os itens 1 a 5 das recomendações, mais `files` e funções aninhadas:

- **Parser**: runtime trocado para o tree-sitter oficial via cgo
  (`github.com/tree-sitter/go-tree-sitter` + gramáticas typescript/tsx/java).
  `.ts` usa a gramática typescript, o resto da família a tsx.
- **`search`**: agrupado por arquivo, definição primeiro, palavras de string
  marcadas, `--path` (prefixo ou glob), `--no-tests`, `--limit`, `--per-file`,
  `--regex` (grep sobre o texto). `refs` ganhou `--path`, `--kind`,
  `--no-tests`, `--limit` e totais por kind.
- **Índice**: palavras dentro de literais de string (`kind = string`),
  acessos a campo/propriedade como refs (`property`), funções aninhadas em
  funções (padrão factory/closure) como símbolos com container.
- **`files [path] --depth N --no-tests`** e `list_files` no MCP: a árvore de
  arquivos indexados com contagem de símbolos.
- **Visibilidade**: anotações/decorators por símbolo, `parse_error` com dica
  em `symbols`/`resolve`, `hint` quando `resolve` não acha nada, membros
  herdados de tipos externos marcados `external`, `snippet --symbol`,
  JSON compacto sem ids.

| repositório | erros de parse | símbolos | edges |
|---|---|---|---|
| spring-petclinic | 5 → **0** | 266 → **318** | 291 → **458** |
| react-hook-form | 137 → **0** | 386 → **832** | 417 → **1.700** |

Verificações pontuais nos mesmos pontos em que os agentes tropeçaram:

- `resolve register` (react-hook-form): antes vazio; agora
  `createFormControl.register`, `src/logic/createFormControl.ts:1696-1798`,
  com 11 callees. `createFormControl.ts` passou de 5 para 58 símbolos.
- `refs telephone` (petclinic): antes vazio; agora 3 acessos de campo
  resolvidos para `Owner.telephone`.
- `search telephone --no-tests`: 1 arquivo, definição marcada `D`, as
  ocorrências em `"{telephone.invalid}"` e `.append("telephone", …)` marcadas
  `s` (string).
- `refs save`: 8 refs `external` (herdadas de `JpaRepository`), antes
  `unresolved`.
- `symbols VisitController.java`: `@Controller`, `@InitBinder`,
  `@ModelAttribute("visit")` visíveis por símbolo.
- Tempo de indexação completa do react-hook-form: 2,9 s (parse 0,8 s,
  resolução 2,1 s).

## Segunda rodada com os agentes (mesmas tarefas, ferramentas novas)

Só o braço codegraph foi repetido, nas duas tarefas em que ele mais perdeu.
Baseline da primeira rodada repetido na tabela para comparação.

| tarefa | rodada | comandos | bytes de saída | ~tokens | confiança |
|---|---|---|---|---|---|
| petclinic 2: adicionar campo em Owner | codegraph v1 | 25 (limite) | 50.263 | 12,6k | 85% Java / 50% resto |
| petclinic 2 | **codegraph v2** | **22** | **41.243** | **10,3k** | **90% / 55%** |
| petclinic 2 | baseline | 20 | 41.599 | 10,4k | 90% |
| react-hook-form 1: register → required | codegraph v1 | 31 | 72.306 | 18,1k | 85% |
| react-hook-form 1 | **codegraph v2** | **23** | **67.931** | **17,0k** | **92%** |
| react-hook-form 1 | baseline | 29 | 23.626 | 5,9k | 95% |

O que mudou na prática, segundo os próprios agentes:

- `resolve register` acertou de primeira a closure `createFormControl.register`
  com callees; `resolve handleSubmit` entregou a bifurcação resolver/embutida.
  Na primeira rodada esses três `resolve` vinham vazios.
- `search telephone` com `in_string` e `definition` mapeou o campo no Java
  inteiro numa chamada; `refs setTelephone` e `refs getTelephone` mostraram
  que o binding é reflexivo e que os consumidores estão nos templates.
- `files` substituiu o `ls` que os agentes pediam; `symbols` com anotações
  dispensou as buscas por `@PostMapping`/`@Column`.
- Nenhum bloqueio real: os momentos de "quis usar grep" foram resolvidos
  com `search --regex` e `symbols`.

Atritos que sobraram (próxima rodada):

1. `resolve` não tem `--no-tests` nem `--kind`: `resolve Field` misturou o
   componente de `app/` com o tipo de `src/types`; callers de classe são
   quase só testes que fazem `new X()`.
2. `files --depth 2` lista arquivos demais para uma primeira olhada
   (`examples/`, `app/`); um `--dirs-only` ajudaria. Cadeias de diretório
   único já colapsam (feito depois desta rodada, junto com exports default
   anônimos nomeados pelo arquivo: `resolve validateField` passa a funcionar
   e callees deixam de se chamar `default`).
3. `symbols` de arquivo grande devolve assinaturas multi-linha inteiras; um
   modo compacto (nome, kind, linhas) reduziria o volume.
4. `refs getTelephone` devolve 0 sem avisar que há consumidores fora do
   índice (templates); a dica deveria dizer que só código-fonte é indexado.
5. Não há pergunta direta "onde X é exposto no objeto retornado"; o agente
   resolveu com `search --regex '^\s*(register|handleSubmit),'`.
6. JSON escapa `<` como `\u003c`.

Onde o codegraph ainda perde para o grep nesta medição: bytes por resposta.
O JSON do `symbols` de um arquivo de 2.300 linhas (58 símbolos com
assinaturas) e a árvore inicial do `files` pesam mais que um `grep -n`. É o
próximo alvo: saída compacta por padrão e `--dirs-only`.

### Ajustes feitos depois da segunda rodada (medidos no react-hook-form)

| saída | antes | depois |
|---|---|---|
| primeira olhada no repo (`files`) | `--depth 2 --no-tests`: 17.532 B | `--dirs-only --no-tests`: **729 B**; default agora `--depth 1`: 5.022 B |
| mapa de `createFormControl.ts` (`symbols`) | 19.663 B | `--compact`: **9.845 B** |
| `resolve validateField` (export default anônimo) | vazio | resolvido, callees com nome do arquivo em vez de `default` |
| `files` no petclinic | `src/main/java/org/...` gastava a profundidade | cadeia única colapsada em um nível |

Com essas duas trocas na sessão do agente do react-hook-form (17,5 KB → 0,7 KB
no `files` e 19,7 KB → 9,8 KB no `symbols`), o total da rodada 2 cairia de
~68 KB para ~41 KB, abaixo do baseline em chamadas (23 contra 29) e no mesmo
patamar em bytes que a tarefa 2 do petclinic. Isso é projeção sobre o log,
não uma terceira rodada.

## Formato de saída: onde os bytes iam e o que mudou

Medido no react-hook-form antes da mudança (JSON via CLI, mesma coisa que o
MCP entregava):

| comando | total | itens | bytes/item | maior campo |
|---|---|---|---|---|
| `search required --path src/logic` | 818 B | 10 hits | 81 | texto da linha |
| `resolve register` | 3.854 B | 13 | 296 | `signature` 27% |
| `symbols createFormControl.ts` | 19.663 B | 58 | 339 | `signature` 30%, `file` repetido 58x |
| `refs validateField` | 49.861 B | 93 | 536 | `signature` do alvo 45% |

E o MCP mandava tudo duas vezes (`content.text` + `structuredContent`):
`find_references` eram 104 KB no fio. O schema das 8 tools custa 5,3 KB
(~1.300 tokens) por turno.

Mudanças (itens 1 a 4 da análise):

1. MCP devolve só texto, no formato compacto compartilhado com a CLI; sem
   `structuredContent`.
2. Itens aninhados (callers, callees, alvos, candidatos) são referências de
   uma linha `nome kind arquivo:início-fim`, sem assinatura. `refs` agrupa por
   arquivo, lista os alvos e candidatos uma vez e devolve a linha do fonte.
3. `symbols` é um outline aninhado: arquivo uma vez, membros indentados sob o
   container, assinatura cortada em 100 caracteres.
4. `max_tokens` em toda tool (padrão 4000) e `--max-tokens` na CLI: corte no
   fim de linha com aviso de quantas linhas ficaram de fora e como estreitar.

Mesmas chamadas, depois (bytes no fio do MCP):

| tool | antes | depois | fator |
|---|---|---|---|
| `resolve_symbol register` | 8.192 B | 1.477 B | 5,5x |
| `find_references validateField` (93 refs) | 104.030 B | 3.714 B | 28x |
| `list_symbols createFormControl.ts` (58 símbolos) | 41.302 B | 7.116 B | 5,8x |
| `list_files dirs_only` | | 505 B | |
| `search_text required --path src/logic` | | 679 B | |

Comparado com o grep equivalente: `grep -n validateField` no repo devolve
356 B para 5 linhas (71 B/linha); o `refs` devolve 93 refs em 3,5 KB
(38 B/ref) já com kind, resolução e alvo. `symbols --compact` de um arquivo de
2.300 linhas custa 6,4 KB de texto contra 6,5 KB de um `grep` por `const`/
`function`, com nome, kind e ranges exatos em vez de linhas cruas.

O que ficou para depois: uma tool única `explore` para reduzir o schema por
turno, dedup de sessão (Probe) e ranking por importância para um mapa inicial
(aider).

## Fechamento: menos rodadas por tarefa

Implementado depois do benchmark, direto dos atritos relatados:

1. **Snippets e corpos numerados** (`1696| const register…`); `snippet
   --no-numbers` desliga. Cita-se `arquivo:linha` sem contar.
2. **`resolve --kind`, `--path`, `--no-tests`**, definições de produção antes
   das de teste (também nos callers/callees), e dica quando tudo foi filtrado.
3. **`refs` com o método chamador** em cada linha (`in Owner.addVisit`), alvo
   encurtado para `Classe.membro` quando há vários.
4. **`resolve --with-refs`** (`include_refs` no MCP): definição, corpo
   numerado, callers, callees e os usos por arquivo numa chamada só.
5. **`referenced by`** no `resolve`: quem expõe ou usa o símbolo sem chamá-lo.
   `return { register, handleSubmit }` agora gera a ref, então `handleSubmit`
   deixa de aparecer sem chamadores (aparece "referenced by createFormControl").
6. **`files`** mostra a cadeia colapsada inteira
   (`main/java/org/springframework/samples/petclinic/` versus `test/...`).

Validação (mesmas tarefas e gabaritos, só o braço codegraph repetido):

| tarefa | rodada | chamadas | tempo | tokens (harness) | bytes de saída | acertos | alucinações |
|---|---|---|---|---|---|---|---|
| T2 register → required | codegraph antes | 24 | 292 s | 90.725 | 38.491 | 8/8 | 0 |
| T2 | **codegraph depois** | **15** | **167 s** | **79.346** | **41.496** | 8/8 | 0 |
| T2 | grep/sed | 22 | 176 s | 73.020 | 27.016 | 8/8 | 0 |
| T3 impacto addPet/addVisit | codegraph antes | 10 | 104 s | 59.511 | 8.046 | 6/6 | 0 |
| T3 | **codegraph depois** | **6** | **73 s** | **54.394** | **3.113** | 6/6 | 0 |
| T3 | grep/sed | 7 | 126 s | 71.171 | 28.225 | 6/6 | 0 |

- T3: o `refs --no-tests` com alvo, método chamador e range resolveu a tarefa
  em duas chamadas mais verificação; 6 chamadas no total, 42% mais rápido e
  24% menos tokens que o grep, com a mesma exatidão.
- T2: as closures aninhadas com `--include-body` numa chamada só derrubaram
  as rodadas de 24 para 15 e o tempo de 292 s para 167 s. Em tokens ainda
  fica 9% acima do grep, porque cada `resolve --include-body` traz corpos de
  100 a 270 linhas: é o custo de "ver a função inteira" contra "grep + sed
  do trecho certo" num arquivo em que o agente já sabe o que procurar.
- Assertividade continuou 100% nas duas.

Sobras relatadas: `--per-file` e `--limit` juntos confundem (o corte por
arquivo vem antes do total); um `snippet --symbol X --tail N` para o
`return` de funções longas; `refs` de vários nomes numa chamada.

## Quarta rodada: esqueleto de função e skill

Pergunta desta rodada: vale a pena uma visão estrutural da função (retornos,
throws, locais, usos) e uma skill que ensine o agente a usar a ferramenta?
Implementado:

1. **`resolve --skeleton`** (`include_skeleton` no MCP): contagens (linhas,
   ramos, laços, funções aninhadas, async), cada `return` e `throw` com a
   linha, `throws` declarado (Java), locais, closures aninhadas indexadas e
   os usos agrupados pela origem do alvo (mesmo arquivo `:linha`, outros
   arquivos `arquivo:linha`, externos, `outer` para campos e estado de
   closure, não resolvidos). Só o escopo próprio conta para return/throw/
   locais; a estrutura vem de um reparse do arquivo na consulta (ms), os
   usos vêm do índice. Filtro de ruído: acesso a parâmetro sem tipo
   (`e.preventDefault()`), leitura de campo de parâmetro (`props.values`),
   membro de expressão complexa sem alvo, propriedade redundante com o
   identificador já listado (`EVENTS.SUBMIT` quando `EVENTS` aparece).
2. **Skill** embutida no binário (`internal/skill/SKILL.md`, 5,8 KB): tabela
   pergunta → tool, receitas de impacto/rastreamento/entender função, como
   ler a saída, quando não usar. `codegraph skill` imprime, `--install` grava
   `.claude/skills/codegraph/SKILL.md` (opt-in, nunca sobrescreve sem
   `--force`); no MCP é o resource `codegraph://skill` e o prompt `guide`.
   Dois testes travam a skill à realidade: todo nome com underscore entre
   crases tem de ser tool ou parâmetro do servidor; todo `codegraph <cmd>
   --flag` citado tem de existir na CLI.
3. Achados dos agentes desta rodada que viraram código no mesmo dia:
   closures de um `export default` anônimo agora são símbolos
   (`validateField.setCustomValidity`); `f.bind/call/apply(...)` conta como
   chamada de `f` (`appendErrors` passou de `callers (0)` para 1); `new Map`,
   `setTimeout` e outros globais do JS deixam de virar refs não resolvidas;
   chamadas em parâmetros tipados (`result.rejectValue()`) e campos usados
   como receptor (`owners.findById()`) aparecem no esqueleto.

Benchmark: mesmas tarefas e gabaritos de antes (T1-T3) mais duas tarefas
novas de "entender uma função" (T4 Java, 29 linhas; T5 TS, 268 linhas),
gabaritos fixados antes de lançar os agentes (`bench/answer-keys.md`). No
braço codegraph a skill foi colada no prompt no lugar da lista de
subcomandos, simulando a skill carregada no workspace.

| tarefa | rodada | chamadas | tempo | tokens (harness) | bytes de saída | acertos | alucinações |
|---|---|---|---|---|---|---|---|
| T1 fluxo de visita (Java) | codegraph v1 | 20 | 228 s | 73.971 | 17.076 | 8/8 | 0 |
| T1 | **codegraph v3 (skill+esqueleto)** | **17** | **202 s** | **71.965** | **13.017** | 8/8 | 0 |
| T1 | grep/sed | 19 | 230 s | 78.290 | 25.643 | 8/8 | 0 |
| T2 register → required (TS) | codegraph v2 | 15 | 167 s | 79.346 | 41.496 | 8/8 | 0 |
| T2 | **codegraph v3** | **16** | **164 s** | **76.463** | **34.542** | 8/8 | 0 |
| T2 | grep/sed | 22 | 176 s | 73.020 | 27.016 | 8/8 | 0 |
| T3 impacto addPet/addVisit | codegraph v2 | 6 | 73 s | 54.394 | 3.113 | 6/6 | 0 |
| T3 | **codegraph v3** | **4** | **64 s** | **53.972** | **2.051** | 6/6 | 0 |
| T3 | grep/sed | 7 | 126 s | 71.171 | 28.225 | 6/6 | 0 |
| T4 entender processFindForm (Java, 29 linhas) | **codegraph v3** | **4** | **77 s** | **55.892** | **4.502** | 9/9 | 0 |
| T4 | grep/sed | 3 | 70 s | 58.343 | 13.091 | 9/9 | 0 |
| T5 entender validateField (TS, 268 linhas) | **codegraph v3** | **8** | **128 s** | **64.824** | **14.266** | 8/8 | 0 |
| T5 | grep/sed | 5 | 118 s | 60.884 | 12.882 | 8/8 | 0 |

Leitura:

- **Impacto (T3)** é onde o grafo mais rende: 4 chamadas, 49% mais rápido e
  24% menos tokens que o grep, 7% dos bytes. O agente usou `refs` para os
  dois nomes e `search` como contraprova; pediu `refs` de vários nomes numa
  chamada.
- **Fluxo (T1)** com skill: 20 → 17 chamadas, 228 → 202 s, 17 → 13 KB de
  saída; contra o grep, 12% mais rápido e 8% menos tokens. O primeiro
  `resolve --skeleton` já entregou o salto para `Owner.addVisit:173` e marcou
  `OwnerRepository.save` como externo. Atrito: o snippet de 8 linhas do
  `resolve` é curto para um método de 16; o agente sugeriu incluir o corpo
  quando a função é pequena.
- **Rastreamento (T2)** ficou estável em chamadas (15 → 16) e tempo, com
  17% menos bytes de saída e 4% menos tokens; contra o grep segue 5% acima
  em tokens. O `outer` do esqueleto de `validateField` (linhas em que
  `INPUT_VALIDATION_RULES` é usado) substituiu um grep por `required`. Atrito
  desta rodada: a lista `nested` do `createFormControl` (52 closures) cortava
  em 30, o agente adivinhou `createFormControl.register`; o corte subiu para
  80 depois da rodada.
- **Entender função curta (T4)**: paridade. Um grep com alternância mais dois
  `sed` resolvem um método de 29 linhas em 3 comandos; o esqueleto não tem o
  que economizar aí (4 chamadas, 34% dos bytes, 4% menos tokens, 10% mais
  tempo).
- **Entender função longa em detalhe (T5)**: o codegraph perde 6% em tokens
  e 11% em bytes. A tarefa pedia todas as regras da função, então o agente
  leu a função inteira em três `snippet` depois do esqueleto; o grep fez
  `cat -n` de quatro arquivos. O esqueleto sozinho custa 30% do `cat`
  (3.227 B contra 10.929 B, medido no teste de cenário), mas quando a
  pergunta é "explique tudo", ele vira uma chamada a mais, não a menos. Esse
  é o limite honesto da feature: ela paga quando se quer *parte* da função.
- Assertividade: 100% nos dez braços, zero alucinações. As duas inferências
  marcadas pelos próprios agentes (semântica de merge/cascade do JPA em T1,
  ordem de spread em T5) estão certas.

Cenários recriados como teste (`test/e2e/scenario_test.go`): cada cenário
roda a mesma pergunta com codegraph e com os comandos grep/sed que o agente
baseline usou, exige os fatos do gabarito nas duas saídas e limita a razão
de bytes codegraph/shell. Três cenários nos fixtures rodam sempre; cinco nos
repositórios reais rodam com `CODEGRAPH_FIELD_REPOS=<dir>` apontando para os
clones nos commits do campo (petclinic `818c413`, react-hook-form
`38efe7b`). Razões medidas: T3 0,18; T4 0,66; T5 como o agente rodou 1,14;
T5 só esqueleto 0,30; T2 0,85.

Sobras para a próxima rodada, na ordem em que os agentes pediram:
`refs` com vários nomes; `resolve` de função curta trazer o corpo inteiro
(ou `--skeleton` implicar corpo abaixo de ~30 linhas); `locals` com o range
de objetos literais grandes (`methods (2198)` sem fim); chamada não
qualificada com sobrecarga (`getPet(petId)` dentro de `Owner`) sem edge de
caller; membros herdados dentro do repositório (`owner.getLastName()` está
em `Person`) ainda `unresolved`.

## Quinta rodada: as cinco sobras

Atacadas na ordem em que os agentes pediram:

1. **`refs` com vários nomes**: `codegraph refs addPet addVisit`; no MCP,
   `name` aceita "addPet, addVisit". Um bloco por nome, `--limit` por nome;
   com vários nomes o JSON vira uma lista.
2. **Função curta traz o corpo**: definições de até 30 linhas vêm inteiras
   sem `--include-body` (o agente do T1 gastou uma chamada para ver as 8
   linhas restantes de um método de 16). Acima disso, a prévia de 8 linhas
   ganha a dica `… N more lines: include-body, skeleton, or snippet a-b`.
3. **Range dos locais**: `locals: methods (2198-2279)` quando o valor ocupa
   várias linhas (objeto literal, classe anônima), para o snippet certo.
4. **Sobrecargas**: as chamadas passam a guardar o número de argumentos e o
   tipo de cada um quando se conhece (literal, variável com tipo declarado,
   `new T()`, cast, `this`), coluna nova no índice (schema v3). O resolvedor
   separa sobrecargas primeiro pela aridade, depois pelos tipos, com boxing
   `int`/`Integer` e widening de literais; o que não se separa fica
   `ambiguous`, nunca chutado. `getPet(petId)` com `petId` int só pode ser
   `getPet(Integer)`; `getPet(name, false)` só a de dois parâmetros.
5. **Membros herdados dentro do repositório**: `owner.getLastName()` sobe
   `Owner → Person` e resolve em `Person.getLastName`; `owner.getId()` chega
   a `BaseEntity.getId` dois níveis acima; uma chamada sem receiver dentro
   da subclasse faz o mesmo. O pai é resolvido no contexto do arquivo dele
   na hora (esse arquivo pode ainda não ter sido resolvido na rodada), com
   cache por rodada e limite de 8 níveis. Um pai fora do índice continua
   tornando o membro `external` (`JpaRepository.save`).

Efeito medido no spring-petclinic (índice completo): zero refs ambíguas
(antes, as 7 chamadas de `getPet` ficavam ambíguas entre as três
sobrecargas); `refs getPet` com as 7 resolvidas para a sobrecarga certa;
`Owner.addVisit` passa a ter `Owner.getPet` entre os callees; o esqueleto de
`processFindForm` mostra `Person.getLastName` e `BaseEntity.getId` em
`uses (other files)` em vez de `unresolved`. No react-hook-form nada mudou
nas contagens (TS quase não tem sobrecarga nem herança de classe).
Cenários de campo continuam passando (T4 0,68; T2 0,87; T3 0,18).

Limite honesto do item 4: uma subclasse passada a um parâmetro da
superclasse não separa a sobrecarga (o comparador só aceita nomes iguais,
boxing e widening), então a ref fica ambígua. É preferível a resolver errado.

Validação com o T1 (mesma tarefa e gabarito): codegraph v4 fez 17 chamadas
em 219 s com 72.983 tokens e 17,8 KB de saída, 8/8 e zero alucinações;
igual ao v3 em chamadas e tokens (17 / 202 s / 71.965). O ganho apareceu na
qualidade, não na contagem: `owner.getPet(petId)` veio resolvido para
`Owner.getPet(Integer)` (o v3 tinha `[ambiguous]`) e `Owner.addVisit` passou
a listar `Owner.getPet` nos callees. Os dois atritos novos relatados:
`resolve` aceita um nome só (custou uma chamada) e `resolve` de uma classe
de 73 linhas mostra 8 linhas, quando um outline dos membros com ranges
seria a resposta certa para classe. São os próximos itens.

## Descoberta com pergunta vaga em português (dado para a busca semântica)

Duas tarefas sem nenhum nome de símbolo, escritas em português, só com o
braço codegraph, para medir se a busca lexical basta quando o agente não
sabe o vocabulário do repositório.

| tarefa | chamadas | tempo | tokens | bytes | achou o gabarito | como traduziu a pergunta |
|---|---|---|---|---|---|---|
| D1 petclinic: regra "visita com data no passado", como o erro chega ao usuário, pista para o navegador | 7 | 111 s | 58.695 | 6.028 | sim (VisitController 100-102, chave `typeMismatch.visitDate`, `minVisitDate` 83-86) | `search 'isBefore\|isAfter\|LocalDate\.now\|minDate' --regex` na primeira chamada |
| D2 react-hook-form: quando a validação roda (digitar vs. sair), como muda após o primeiro submit, modos e onde estão | 15 (limite) | 174 s | 66.039 | 8.850 | sim (skipValidation.ts 3-20, getValidationModes.ts 4-10, VALIDATION_MODE constants.ts 10-16, createFormControl 201-202 e 1203-1209) | `search onTouched`, `search reValidateMode` (palpite), `search isSubmitted` |

O que os relatórios dizem sobre busca por significado: em D1 "não fez
falta; o vocabulário Java da regra era previsível". Em D2 "teria ajudado só
no passo 2 se eu não conhecesse `onBlur`/`onChange`; 'como muda após o
envio' exigiu um palpite (`reValidateMode`) que deu certo". Os bloqueios
reais foram outros: `.properties` e templates fora do índice (D1), `search`
sem contexto nem o símbolo que envolve o hit (D1 e D2), `snippet` sem dizer
em que função está (D2), e o agente não confiar que `resolve skipValidation`
resolve um `export default` anônimo (resolve, mas a skill não dizia).

## Sexta rodada: próximos passos 1, 2, 3, 5, 6 e 7

Implementados de uma vez, com testes, sem nova rodada de agentes (a
validação foi por teste de cenário e por inspeção nos repositórios de campo):

1. **Atritos dos agentes**: `resolve` de classe ou interface longa responde
   com `members (n)` (métodos e campos com ranges) em vez de 8 linhas;
   `resolve` e `refs` aceitam vários nomes; `search --context N` e cada hit
   diz o símbolo que o envolve (`s 101 in VisitController.processNewVisitForm:97-112`);
   `snippet` diz em que símbolo está (`Money.java:6-8 (in Money.plus:6-8)`).
2. **Arquivos sem código no índice lexical**: `.properties`, `.yml`, `.html`,
   `.sql`, `.md`, `.xml`, `.json`, `.txt`, `.toml` entram só por palavras
   (marca `t`), até 512 KB, sem lockfiles; `skip_text: true` desliga. No
   petclinic `search visita` acha `messages_pt.properties:9`, a parede do
   T1 e do D1. O índice foi de 50 para 102 arquivos.
3. **Sub-tokens e comentários**: `search date` acha `visitDate` e
   `VISIT_DATE` (marca `~`, em minúsculas, depois dos exatos); palavras de
   comentário entram com a marca `c`.
5. **Precisão**: `tsconfig.json` (`paths`, `baseUrl`, `extends` relativo,
   comentários e vírgulas finais tolerados) reescreve imports não relativos
   antes de marcá-los externos; chamadas encadeadas `a.b().c()` seguem o tipo
   declarado de cada passo (retorno de método ou tipo de campo; arrays e
   Promise viram external; passo sem tipo declarado fica unresolved; sobrecargas
   de mesmo tipo de retorno não travam a cadeia), o que exigiu indexar
   propriedades de interface TS como símbolos e a coluna `receiver_path`
   (schema v4); uma subclasse passada a um parâmetro da superclasse agora
   separa a sobrecarga (`admit(dog)` vai para `admit(Animal)`).
6. **MCP**: `codegraph mcp --single-tool` expõe uma tool `explore` com
   `action`: o `tools/list` cai de 7.369 para 3.025 bytes por turno, mesma
   saída; dedup de sessão: uma chamada idêntica a outra já respondida volta
   `same as call #N (…): pass fresh=true to resend`, limpo no `reindex`.
7. **Empacotamento**: `codegraph version` (ldflags), `codegraph doctor`
   (git, runtime cgo, init, schema, índice desatualizado, `.gitignore`,
   skill, tsconfig, e a linha do `claude mcp add`), `Makefile`, workflows de
   CI e de release por SO/arquitetura (cgo compila em cada runner), fórmula
   Homebrew que compila do fonte, e o guia "Como validar num projeto seu"
   no README.

Efeito no petclinic (índice completo): `visit.getDate().isAfter(...)` e
`ownersResults.iterator().next()` deixam de ser `unresolved` e viram
`external` com o nome da cadeia; `search date --context 1` mostra a regra
com o método envolvente sem precisar de `snippet`. No react-hook-form, as
refs não resolvidas caíram de 20.286 para 19.276 e as externas subiram de
8.435 para 9.463 (cadeias e arrays). Cenários de campo continuam passando
(T3 0,18; T4 0,68; T5 esqueleto 0,30; T2 0,90).

## Sétima rodada: edição de arquivos com Haiku e Sonnet, com e sem codegraph

Pergunta: o codegraph ajuda um agente a se localizar numa tarefa de
*alteração* de código, e o ganho depende do modelo? Três cenários com
gabarito (`bench/answer-keys.md`), doze rodadas: 3 cenários × {Haiku 4.5,
Sonnet 5} × {codegraph, grep/sed}. Cada rodada numa cópia limpa do
repositório, com índice pronto. As duas ferramentas de edição eram as
mesmas (Edit, com Read limitado a 60 linhas em torno do ponto já
localizado); só a navegação mudava. O diff final foi avaliado por script
(`edits/eval_edits.py`) contra o gabarito: itens obrigatórios, itens
proibidos, arquivos extras.

- E1 (Java): renomear `Owner.addVisit` para `scheduleVisit` na definição e nos
  2 callers, sem tocar em `Pet.addVisit` (homônimo em 3 lugares).
- E2 (Java + texto): aceitar visita hoje: regra no controller, data mínima do
  formulário, teste do caso inválido, mensagens em inglês e português.
- E3 (TS): renomear a closure interna `onChange` de `createFormControl`
  (300 `onChange` no repositório; `field._f.onChange` e
  `VALIDATION_MODE.onChange` não podem mudar; a chave pública `onChange`
  continua).

| cenário | modelo | braço | navegação | tools total | tempo | tokens | bytes lidos | acertos |
|---|---|---|---|---|---|---|---|---|
| E1 | Haiku | codegraph | 2 | 8 | 58 s | 47,5k | 3,8 KB | 3/3, 0 proibidos |
| E1 | Haiku | grep | 9 | 15 | 95 s | 51,6k | 7,5 KB | 3/3 |
| E1 | Sonnet | codegraph | 3 | 9 | 75 s | 64,9k | 5,9 KB | 3/3 |
| E1 | Sonnet | grep | 4 | 10 | 83 s | 67,6k | 9,5 KB | 3/3 |
| E2 | Haiku | codegraph | 8 | 19 | 124 s | 60,5k | 18,5 KB | 5/5 |
| E2 | Haiku | grep | 11 | 21 | 120 s | 59,8k | 20,4 KB | 5/5 |
| E2 | Sonnet | codegraph | 5 | 14 | 106 s | 72,6k | 17,6 KB | 5/5 |
| E2 | Sonnet | grep | 9 | 18 | 165 s | 77,7k | 18,0 KB | 5/5 |
| E3 | Haiku | codegraph | 6 | 11 | 81 s | 53,4k | 15,4 KB | 3/3 |
| E3 | Haiku | grep | 9 | 13 | 87 s | 49,8k | 68,9 KB | 3/3 |
| E3 | Sonnet | codegraph | 7 | 11 | 120 s | 70,4k | 14,3 KB | 3/3 |
| E3 | Sonnet | grep | 5 | 9 | 64 s | 61,6k | 3,9 KB | 3/3 |

"Navegação" conta as chamadas do wrapper, incluindo o `git diff` final;
"bytes lidos" é a soma das saídas dessas chamadas. Somas por braço, seis
rodadas cada: codegraph 31 chamadas de navegação, 564 s, 369,3k tokens,
75,5 KB; grep 47 chamadas, 614 s, 368,1k tokens, 128,2 KB.

Leitura:

- **Correção**: 12/12, nenhum item proibido tocado, nenhum arquivo extra.
  Nestes tamanhos de mudança (3 a 5 pontos), grep e codegraph levam os
  dois modelos ao mesmo diff. O que difere é o caminho.
- **Haiku** é quem mais ganha com o codegraph: 29 → 16 chamadas de navegação
  e 97 → 38 KB lidos (o `cat` de um arquivo de 62 KB no E3 virou um
  `resolve` de 15 KB; no E1 uma chamada de `resolve --with-refs` resolveu a
  tarefa). Tempo 302 → 263 s. Tokens iguais (161k nos dois).
- **Sonnet** com grep já é enxuto (E3: 5 comandos, 3,9 KB); o codegraph
  reduz chamadas (18 → 15) e tempo (312 → 301 s), mas os tokens ficam iguais
  (207k) e no E3 lê mais bytes, porque pediu o corpo inteiro da closure de
  165 linhas quando o grep bastava.
- **Tokens não se movem** em nenhum braço: com 8 a 21 chamadas por rodada e
  um relatório longo no fim, o custo fixo por turno domina, como nos
  benchmarks de leitura.
- **Atrito novo, dos dois Sonnet com codegraph**: depois de editar, o agente
  tentou confirmar a edição com `refs`/`resolve` e o índice ainda mostrava o
  código antigo; gastou 3 chamadas no E3 antes de recorrer ao `git diff`.
  Corrigido no mesmo dia: toda consulta (CLI e MCP) roda uma indexação
  incremental antes de responder e descarta o dedup de sessão quando algo
  mudou; `--no-auto-index` desliga.

Conclusão honesta: em tarefas de edição pequenas, o codegraph não muda o
resultado nem o custo em tokens; muda o caminho (um terço a menos de
chamadas de navegação, 40% menos bytes lidos) e o ganho é maior no modelo
menor. O cenário em que ele deveria mudar o resultado, alterações grandes
onde o grep deixa call sites para trás, não foi testado aqui: as tarefas
tinham 3 a 5 pontos e os dois braços acharam todos.

## Oitava rodada: construir 1 a 5, com Python e Go

Sem rodada de agentes; validação por testes de unidade, e2e (dois fixtures
novos, `go-service` e `py-service`) e inspeção nos repositórios de campo.

1. **Inferência de tipo onde não há anotação**: funções TS sem retorno
   anotado ganham um `return_hint` (schema v5) lido do primeiro `return`
   (`new X()`, campo tipado, `f()` um nível); `const x = f()` / `x := f()`
   / `x = f()` guardam `call:f` e o resolvedor troca pelo retorno de `f`;
   `x := pkg.Zero` guarda `var:pkg.Zero` e usa o tipo da variável. Em Java e
   TS, `List<X>`/`X[]`/`Optional<X>`/`Promise<X>` entregam X em `get()`,
   `orElseThrow()`, `find()`, `pop()`, `then()`; `stream()`/`filter()`
   mantêm o container; `map()` desiste (external, sem chute).
2. **`resolve --around <linha> --context N`** (`around` no MCP): janela de
   ±8 linhas em vez do corpo, com `… a-b` marcando o que ficou de fora.
3. **Watcher no MCP** (fsnotify): a consulta só caminha o repositório quando
   algo mudou desde a anterior; acima de 1.500 diretórios ou sem descritores
   volta ao modo anterior. Edições feitas pelas tools reindexam na hora.
4. **Python e Go** com o mesmo pipeline: extractors por tipo de nó, kinds
   existentes (struct = class, `def` em classe = method), imports resolvidos
   (Go: `go.mod` + diretório como pacote, métodos espalhados por arquivos,
   struct embutida = herança; Python: relativos e absolutos, re-exports em
   `__init__.py`, `self.x = …` vira campo, bases = herança, alias de módulo),
   esqueleto (`panic`/`raise` como throw, `go`/`await` como async),
   comentários `#`, strings triplas e raw strings no scanner, testes por
   convenção (`_test.go`, `test_*.py`, `conftest.py`).
5. **Edição por símbolo**: `codegraph edit replace|insert-before|insert-after
   <file> <symbol>` (texto por `--text` ou stdin) e as tools
   `replace_symbol_body`, `insert_before_symbol`, `insert_after_symbol`
   (também como ações do `explore`). Recusa arquivo desatualizado, exige
   nome qualificado quando há homônimos, reindexa em seguida.

Limites honestos: Go não tem interfaces implícitas (quem satisfaz uma
interface não aparece em `implemented by`) nem genéricos além do nome
base; Python não infere o tipo do alvo de um `for` nem lê `typing`
avançado; nas duas, uma função sem anotação e sem `return` reconhecível
fica sem tipo, e a cadeia para ali, `unresolved`, sem chute.

### Checagem em repositórios reais (Go e Python)

Sem rodada de agentes: índice completo em dois repositórios de fora dos
fixtures, `spf13/cobra` v1.10.2 (Go, 36 arquivos) e `jinja2` 3.x (Python,
25 arquivos), com contagem de refs por resolução direto no SQLite.

| repo | parse errors | refs | resolved | external | unresolved (antes → depois) |
|---|---|---|---|---|---|
| cobra | 0 | 8.419 | 5.562 | 2.465 | 1.826 → 390 (4,6%) |
| jinja2 | 0 | 7.730 | 4.106 | 2.208 | 1.882 → 1.403 (18%) |

O que os "antes" escondiam, corrigido com teste em cada caso:

- Python: `from . import nodes` montava o submódulo como `..nodes` (pacote
  pai) e caía no `__init__.py`; toda anotação `nodes.Expr` ficava
  unresolved (59 só em jinja2).
- Go: a chave de um literal composto (`&Command{Use: "x"}`,
  `[]Cmd{{Use: "y"}}`) era tratada como identificador solto (700 refs em
  cobra); agora é acesso ao campo do tipo do literal, e `refs Use` lista
  quem preenche o campo. Chaves de `map[K]V{KeyA: 1}` continuam
  identificadores.
- Go: `type Completion = string` (alias) não virava símbolo; `buf :=
  new(bytes.Buffer)` e `make([]T, n)` não davam tipo; `for _, c := range
  cmds` não herdava o elemento de `cmds []*Cmd`; `err.Error()` aparecia
  como unresolved em vez de sumir (tipo predeclarado).
- Python: `**kwargs: t.Any` (splat com anotação) não contava como local e
  cada uso virava um identificador unresolved.
- Resolvedor: uma ref sem receptor nominal mas com tipo (`{Use: …}`) caía
  no caminho de "método não qualificado" e ignorava o tipo.

O que sobra em jinja2 é o limite já declarado: `self.stream.current` onde
`self.stream = environment._tokenize(...)` (chamada num parâmetro tipado,
guardada em campo: o hint `call:` só cobre funções e módulos), métodos de
builtins em locais sem tipo (`append`, `join`, `expect` de um `TokenStream`
vindo de um `for`), e `for` sem tipo de alvo. Em cobra sobram campos de
`*pflag.Flag` (dependência externa, `Changed`/`Lookup`) e structs anônimas
de tabelas de teste (`desc`, `expectedOutput`), ambos sem alvo no índice.

## Nona rodada: edição por símbolo (meta: passar o Serena)

Sem rodada de agentes. Além dos testes, compiladores serviram de oráculo em
cópias de repositórios reais: um rename só conta como certo se o código
continua compilando. A máquina não tem JRE nem `node_modules`, então Java e
TypeScript foram checados lendo os previews no petclinic e no react-hook-form.

O que entrou:

- **`replace-in` / `replace_in_symbol`**: troca um trecho exato dentro de um
  símbolo. Precisa aparecer uma vez (ou `all`) e reescreve só as linhas que
  mudam.
- **`delete` / `delete_symbol`**: leva o comentário de documentação e a linha
  em branco que sobraria, respeitando o espaçamento do arquivo. Recusa enquanto
  houver referência ao símbolo ou a membros dele e diz onde; `force` apaga e
  lista o que ficou pendurado.
- **`rename` / `rename_symbol`**: declaração, refs resolvidas, receptor de
  acesso estático (`Money.ZERO`) e re-exports de TypeScript. Homônimos
  resolvidos para outro símbolo ficam intocados. Usos não resolvidos que talvez
  sejam ele voltam com a linha. Menções em comentários, strings e arquivos de
  texto, e linhas com o nome em sintaxe sem ref, viram nota. Nome já declarado
  no escopo é recusado; `name:line` escolhe uma sobrecarga; export default de
  TypeScript muda só no próprio arquivo, e o anônimo é recusado.
- **`preview`** em todas as ações: diff com a linha original; acima de 40
  mudanças, três por arquivo.
- **Verificação na mesma resposta**: depois de reindexar, quantas referências
  continuam resolvendo, quais quebraram e se o arquivo deixou de parsear.
- Todos os arquivos são calculados em memória antes de gravar o primeiro.

### Oráculos

| repositório | edições | linhas | oráculo | resultado |
|---|---|---|---|---|
| cobra v1.10.2 (Go) | 6 renames: tipo, método, campo com 395 refs, função, função privada, alias | 513 em 22 arquivos | `go build` e `go vet`, pacotes de teste incluídos | compila |
| jinja2 (Python) | 4 renames e 1 `replace-in` | 145 | `compileall` e render com herança, macro, loop e filtro | renderiza igual |

Cada rodada de oráculo achou algo que os fixtures não mostravam, e cada achado
virou correção com teste:

- **Java**: em `Money sum = Money.ZERO;` o tipo mudava e o receptor não, porque
  o receptor de acesso estático não tem ref própria.
- **Go**: `rootCmd := c.Root()`, `for _, cmd := range c.commands`, parâmetro
  variádico e variável de pacote como receptor ficavam sem tipo. O rename
  listava as linhas, mas o código não compilava. A local agora vira a cadeia
  que a define, respeitando o escopo; unresolved no cobra caiu de 390 para 185.
- **Go**: chaves de `&cobra.Command{Use: …}` num pacote de teste externo não
  eram refs, e o rename passava por elas em silêncio. Viraram refs, e a rede de
  segurança passou a apontar qualquer linha com o nome que nenhuma ref cobre.
- **TypeScript**: export default (quem importa usa nome local), re-export sem
  ref, e o caminho do módulo com o mesmo nome confundido com a referência.
- **Java**: sobrecargas sem como escolher uma; o nome qualificado pegava a
  primeira sem avisar.

### Custo para o agente

| caso | medida |
|---|---|
| mudar 1 linha de `createFormControl` (2.141 linhas) | 59.860 B com `replace_symbol_body`, cerca de 54 B com `replace_in_symbol` |
| preview de um rename de 377 linhas | passava de 42 KB; agora três mudanças por arquivo |
| rename aplicado de 18 linhas em 6 arquivos (react-hook-form) | 809 B, com verificação |
| schema MCP por turno | 12,5 KB com as 14 tools; 4,0 KB com `--single-tool` |

### Contra o Serena, por funcionalidade (não medido lado a lado)

O codegraph agora faz o que o Serena não descreve: verificação dos callers e do
parse na mesma resposta, preview, delete que recusa com referências, troca de
trecho escopada ao símbolo, recusa de nome ocupado e lista explícita do que
ficou de fora. O Serena segue à frente em dois pontos: o rename dele passa pelo
language server, que é um verificador de tipos de verdade em Java e TypeScript,
e ele tem edição livre por linha e por regex. Onde o language server não
entende o código (templates, reflexão, strings), os dois param no mesmo lugar;
o codegraph ao menos aponta as menções.

