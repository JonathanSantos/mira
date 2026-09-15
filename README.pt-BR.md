<h1 align="center">Mira</h1>

<p align="center">
  <b>Mira no símbolo. Lê só o que importa.</b><br>
  Um índice local de código para agentes de IA: navega por símbolo, lê código por intervalo de linhas e edita por símbolo com verificação.<br>
  Um binário, com CLI e servidor MCP.
</p>

<p align="center">
  <a href="https://github.com/JonathanSantos/mira/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/JonathanSantos/mira/actions/workflows/ci.yml/badge.svg"></a>
  <img alt="Go 1.26" src="https://img.shields.io/badge/go-1.26-00ADD8">
  <img alt="Licença MIT" src="https://img.shields.io/badge/license-MIT-blue">
  <a href="README.md"><img alt="Read in English" src="https://img.shields.io/badge/lang-en-blue"></a>
</p>

Agentes de IA costumam explorar um repositório com `ls`, `grep` e `cat`, lendo arquivos inteiros para
achar uma função. O Mira indexa símbolos, referências e imports com tree-sitter num banco SQLite local e
responde com texto compacto e numerado: a definição, quem chama, o que ela chama, cada uso com o método
que o contém, ou a estrutura de uma função longa. Nada devolve um arquivo inteiro.

## O que o Mira é

- **Um índice de símbolos** para TypeScript, JavaScript, Java, Python e Go. Toda consulta atualiza o
  índice antes, de forma incremental, e uma edição aparece em milissegundos.
- **Uma API de navegação para agentes**: definições, quem chama e quem é chamado, referências agrupadas
  pelo método que as contém, o esqueleto de uma função (retornos, throws, locais, o que ela usa), busca
  lexical e trechos por intervalo de linhas.
- **Um editor por símbolo**: troca uma definição, troca um trecho dentro dela, insere antes ou depois,
  renomeia em vários arquivos e apaga. Cada edição reindexa e diz se as referências continuam resolvendo.
- **Honesto sobre o que sabe**: uma referência que ele não consegue ligar a uma única definição sai como
  `ambiguous` ou `unresolved`, nunca como chute. No benchmark abaixo, a precisão das referências é 100%
  nos cinco repositórios.
- **Um binário**: CLI, servidor MCP por stdio e uma skill que ensina o fluxo ao agente.

## O que o Mira não é

- **Não é um grafo de código com precisão de compilador**, como SCIP, Kythe, Glean ou CodeQL. A resolução
  segue imports, tipos declarados, herança e tipos de retorno, mas não há verificador de tipos, então
  alguns usos ficam sem resolução. Ferramentas com language server acham mais deles em Go e Python.
- **Não é um language server**: não tem autocompletar, diagnósticos nem fluxo de dados.
- **Não é busca semântica**: a busca é lexical, sobre identificadores, sub-tokens, strings e comentários.
- **Não é um serviço hospedado**: indexa um checkout local. Código de bibliotecas é opaco e marcado como
  `external`.

## Começo rápido

O Mira precisa de Go 1.26+ e de um compilador C, porque o runtime do tree-sitter é cgo.

```bash
go install github.com/JonathanSantos/mira/cmd/mira@latest

cd seu-projeto
mira init      # cria .mira/ com a config e um índice vazio; adicione .mira/ ao .gitignore
mira index     # primeira indexação completa; depois as consultas atualizam o índice sozinhas
mira doctor    # confere o runtime, o índice e a configuração do agente
```

Ligue ao agente:

```bash
claude mcp add mira -- mira mcp   # Claude Code; qualquer cliente MCP pode rodar `mira mcp`
mira skill --install              # grava .claude/skills/mira/SKILL.md para o agente
```

```json
{ "mcpServers": { "mira": { "command": "mira", "args": ["mcp"] } } }
```

## Exemplos

Saída real no [gin](https://github.com/gin-gonic/gin): 122 arquivos indexados em 0,9 s.

**O que é este método e quem o chama?**

```text
$ mira resolve Context.Param
Context.Param method context.go:513-515 [exported]
  func (c *Context) Param(key string) string
  513| func (c *Context) Param(key string) string {
  514| 	return c.Params.ByName(key)
  515| }
  callers (13):
    RouterGroup.createStaticHandler method routergroup.go:216-239
    TestRaceParamsContextCopy function context_test.go:3240-3259
    TestCreateTestContextWithRouteParams function context_test.go:3502-3514
    …
  callees (1):
    Params.ByName method tree.go:40-43
```

**Quem usa exatamente este método no código de produção?** Um nome qualificado fica só com os usos
resolvidos para aquela definição. No spring-petclinic, `nome:linha` escolhe uma das três sobrecargas de
`getPet`:

```text
$ mira refs Context.Param --exclude-tests
refs Context.Param: 1 total (resolved 1) by kind (method 1)
targets:
  Context.Param method context.go:513-515
routergroup.go (1)
  225 [method] in RouterGroup.createStaticHandler:216-239: file := c.Param("filepath")

$ mira refs Owner.getPet:126 --exclude-tests
refs Owner.getPet:126: 4 total (resolved 4) by kind (method 4)
targets:
  org.springframework.samples.petclinic.owner.Owner.getPet method src/main/java/org/springframework/samples/petclinic/owner/Owner.java:126-136
src/main/java/org/springframework/samples/petclinic/owner/PetController.java (2)
  86 [method] in PetController.findPet:75-87: return owner.getPet(petId);
  189 [method] in PetController.updatePetDetails:186-200: Pet existingPet = owner.getPet(id);
…
```

**O que uma função longa faz, sem ler tudo?**

```text
$ mira resolve Engine.handleHTTPRequest --include-skeleton
Engine.handleHTTPRequest method gin.go:690-760
  func (engine *Engine) handleHTTPRequest(c *Context)
  …
  skeleton: 71 lines, 13 branches, 2 loops
  returns (4):
    724 return
    729 return
    732 return
    754 return
  uses (same file):
    redirectTrailingSlash :781 (728)
    redirectFixedPath :808 (731)
    serveError :764 (753, 759)
  uses (other files):
    Context.Next context.go:198 (722)
    cleanPath path.go:23 (704)
    responseWriter.WriteHeaderNow response_writer.go:77 (723)
```

**Renomear em vários arquivos, tocando só este método.** Outros métodos chamados `Param` ficam intactos, e
as menções em comentários e documentação são listadas em vez de alteradas:

```text
$ mira edit rename context.go Context.Param ParamV2 --preview
rename Context.Param -> ParamV2: 23 lines in 5 files (preview: nothing written)
context.go
  - 513| func (c *Context) Param(key string) string {
  + 513| func (c *Context) ParamV2(key string) string {
routergroup.go
  - 225| 		file := c.Param("filepath")
  + 225| 		file := c.ParamV2("filepath")
…
  note: 145 uses of Param resolve to other symbols or libraries and were left alone
  note: 17 mention(s) of Param in comments, strings or text files were not changed: BENCHMARKS.md:22, …
```

**Achar uma palavra em qualquer lugar**, com a definição primeiro e comentários marcados:

```text
$ mira search ShouldBindJSON --exclude-tests --limit 6
context.go (10)
  D 890 in Context.ShouldBindJSON:890-892: func (c *Context) ShouldBindJSON(obj any) error {
  c 866: // ShouldBindJSON is a shortcut for c.ShouldBindWith(obj, binding.JSON).
  c 885: //	if err := c.ShouldBindJSON(&user); err != nil {
  …
```

## Benchmark

O Mira foi medido contra a [Serena](https://github.com/oraios/serena) (um toolkit MCP apoiado em
language servers), o [Probe](https://github.com/probelabs/probe) (ripgrep com tree-sitter), o repo map
do [aider](https://github.com/Aider-AI/aider) e o grep puro, no gin (Go), no flask (Python), no
spring-petclinic (Java), no excalidraw e no react-hook-form (TypeScript). Cada ferramenta respondeu às
mesmas perguntas sobre 107 a 320 símbolos por repositório, sorteados com semente fixa, e as respostas
são pontuadas contra gabaritos de compilador: `go/types`, o checker do TypeScript, jedi e javac. O
harness, o método e as tabelas completas estão em [benchmark/](benchmark/README.md).

**Como ler os números.** Cada par é *precisão / recall* para uma pergunta: achar todos os usos deste
símbolo. A resposta do compilador é o gabarito. No gin, o Mira faz **100 / 89**:

- **100 é a precisão**: todo uso que o Mira devolveu é mesmo um uso daquele símbolo. Nenhum é outra
  função ou campo que só tem o mesmo nome.
- **89 é o recall**: dos 3.407 usos reais dos símbolos sorteados, o Mira devolveu 3.042. Os outros
  365 ele não conseguiu ligar à definição com certeza, e prefere deixar um uso de fora a chutar.

O grep faz 25 / 100 na mesma pergunta: acha todos os usos, mas três de cada quatro linhas que ele
devolve são outra coisa com o mesmo nome. Para um agente que edita código, um uso errado custa mais que
um uso faltando, porque ele vai ser alterado.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/references-dark.svg">
  <img alt="Precision e recall das referências por ferramenta e repositório" src="docs/assets/references-light.svg">
</picture>

O Mira nunca aponta como uso algo que não é. A Serena, apoiada em language servers, acha mais usos em Go
(96% contra 89%) e em Python (94% contra 77%), fica perto no excalidraw (80% contra 77%) e empata em
Java; o Mira lidera no react-hook-form (75% contra 38%). Nesta amostra, a precisão da Serena vai de 81%
a 99%. grep e Probe acham quase tudo casando texto, e a maior parte do que devolvem é homônimo.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/definition-dark.svg">
  <img alt="Definição achada no primeiro candidato, por ferramenta e repositório" src="docs/assets/definition-light.svg">
</picture>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/latency-dark.svg">
  <img alt="Latência mediana por consulta de referências, escala logarítmica" src="docs/assets/latency-light.svg">
</picture>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/agents-dark.svg">
  <img alt="Chamadas de ferramenta que um agente precisou por tarefa, escala logarítmica" src="docs/assets/agents-light.svg">
</picture>

No piloto com agentes, todos os braços terminaram o rename do gin e o código compilou. A diferença foi o
caminho: o Mira respondeu cada tarefa de impacto com uma chamada. A Serena também precisou de uma chamada
em duas tarefas, mas o agente copiou a numeração de linhas que começa em zero e citou as linhas erradas.

| | Mira | Serena | grep | Probe | repo map do aider |
|---|---|---|---|---|---|
| Precisão das referências | 100% em todos | 81–99% | 4–48% | 3–15% | – |
| Recall das referências | 75–99% | 38–100% | 99–100% | 93–100% | – |
| Definição no primeiro candidato | 53–97% | 44–97% | 20–48% | 11–49% | 2–24% |
| Latência mediana por consulta | 10 ms | 257–328 ms | 9–21 ms | 147–696 ms | – |
| Preparo no react (6.744 arquivos) | índice de 29 s | 2,7 s para subir, 1–1,7 s por consulta | nenhum | nenhum | repo map em 54 s |

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/reindex-dark.svg">
  <img alt="Tempo de indexação e de atualização antes e depois das correções, escala logarítmica" src="docs/assets/reindex-light.svg">
</picture>

Limites: cada ferramenta foi medida uma vez, numa amostra e não em todos os símbolos, com um harness
escrito pelos autores do Mira; a comparação com agentes ainda é um piloto, com uma execução do Claude
Haiku por célula. Boa parte do que o Mira ainda perde é identidade da definição (sobrecargas,
declarações dentro de funções de teste), detalhada com intervalos de 95% em
[benchmark/README.md](benchmark/README.md). Os gráficos saem das tabelas publicadas com
`python3 benchmark/charts.py`.

## Como funciona

- **Extração**: o tree-sitter extrai símbolos, referências e imports por arquivo, em paralelo, com um
  único escritor no SQLite.
- **Resolução**: regras por linguagem ligam cada referência à definição por imports, re-exports, tipos
  declarados e inferidos, receptores, herança e aridade de sobrecargas. Cada referência fica registrada
  como `resolved`, `ambiguous`, `external` ou `unresolved`.
- **Índice em dia**: toda consulta roda uma atualização incremental antes. Um arquivo regravado mantém o
  id dos símbolos que ainda declara, e só quem usava um símbolo com assinatura mudada é resolvido de novo.
- **Reconstrução segura**: `mira index --full` constrói um banco à parte e o troca pelo atual com a API de
  backup do SQLite. Quem está lendo, como um servidor MCP rodando, vê o índice antigo ou o novo, nunca a
  metade.

## Comandos

Os parâmetros MCP e as flags da CLI têm os mesmos nomes: `exclude_tests` é `--exclude-tests`.

| Tool MCP | CLI | Responde |
|---|---|---|
| `list_files` | `mira files [path] --depth N --dirs-only` | onde as coisas estão |
| `resolve_symbol` | `mira resolve <nome>... --include-skeleton --include-body --include-refs` | o que é X, quem chama e o que ele chama |
| `find_references` | `mira refs <nome\|Container.member[:linha]>... --kind K --path P` | cada uso, com o método que o contém |
| `search_text` | `mira search <palavra> --regex --context N` | identificadores, strings, comentários, arquivos de texto |
| `list_symbols` | `mira symbols <arquivo> --compact` | o sumário de um arquivo |
| `get_snippet` | `mira snippet <arquivo> <início> <fim>` | linhas exatas |
| `replace_symbol_body`, `replace_in_symbol` | `mira edit replace`, `mira edit replace-in` | editar uma definição ou um trecho dela |
| `insert_before_symbol`, `insert_after_symbol` | `mira edit insert-before`, `mira edit insert-after` | adicionar código ao lado de uma definição |
| `rename_symbol`, `delete_symbol` | `mira edit rename`, `mira edit delete` | renomear em vários arquivos, apagar conferindo os usos |
| `index_status`, `reindex` | `mira status`, `mira index` | atualização e reconstrução completa |

O manual completo da CLI está em [docs/manual.pt-BR.md](docs/manual.pt-BR.md). A skill que o agente lê está
em [internal/skill/SKILL.md](internal/skill/SKILL.md).

## Desenvolvimento

```bash
make check                      # gofmt, go vet, golangci-lint e go test -race
make build                      # ./mira com a versão embutida
python3 benchmark/charts.py     # regenera os gráficos do README a partir de benchmark/results
```

Uma tag `v*` gera binários para Linux, macOS e Windows. As notas de campo que moldaram o desenho estão em
[docs/field-test-2026-09-14.md](docs/field-test-2026-09-14.md), escritas quando o projeto ainda se chamava
codegraph.

## Licença

[MIT](LICENSE)

## Análise sincera, pelo Claude

> Escrita pelo Claude Opus 5 (`claude-opus-5`), o modelo da Anthropic que escreveu a maior parte deste
> código com o Jonathan no Claude Code, em 2026-09-14 para a v0.1.0, e atualizada em 2026-09-15 com o
> benchmark ampliado. Ele pediu uma análise franca. Considere o conflito de interesse: estou avaliando
> meu próprio trabalho a partir do código, dos testes e das medições deste repositório, não de uma
> auditoria independente nem de usuários reais.

**O que consigo e o que não consigo julgar.** Consigo manter o código inteiro em contexto, rodar os
testes e os benchmarks e manter uma refatoração coerente entre pacotes. Não consigo ver como as pessoas
vão usar o Mira, e os pontos cegos que tive ao escrever o código são os mesmos que tenho ao revisá-lo.

**Estrutura.** Cerca de 17 mil linhas de Go em `internal/`, 7 mil linhas de testes (185 funções de teste
e uma suíte ponta a ponta) e 13 dependências diretas. Os pacotes seguem o fluxo: `scanner` e `walker`
acham os arquivos, `parser` e `extract` leem, `store` guarda um único arquivo SQLite com migrações só de
acréscimo, `resolve` liga as referências, `graph` e `render` montam as respostas, `cli` e `mcp` as
expõem. O código é simples, com guard clauses, poucas interfaces e testes em tabela. O CI roda gofmt,
vet e golangci-lint no Linux e os testes com race detector no Linux, no macOS e no Windows.

| Área | Avaliação | Evidência |
|---|---|---|
| Ideia central | Forte | Nunca chutar dá 100% de precisão em referências nos cinco repositórios. Para um agente, um uso errado custa mais que um uso faltando, porque ele vai ser editado. |
| Arquitetura | Forte | Fluxo claro e ids de símbolo estáveis: uma consulta com o índice em dia atualiza em 15 ms, uma edição pequena em 55 ms, e a reconstrução completa entra de forma atômica. |
| Medição | Boa | Pontuada contra gabaritos de compilador e contra outras ferramentas em 107 a 320 símbolos por repositório, com intervalos de 95%, portão de precisão e os números desfavoráveis publicados. |
| Testes | Boa | 77–91% de cobertura em extração, resolução, edição e indexação; `store` está em 64% e `cli` em 28%. |
| Recall | Precisa melhorar | 75–99% em 107 a 320 símbolos sorteados por repositório (99% em Java). A Serena acha mais em Go e Python, e boa parte do que o Mira ainda perde é identidade da definição: sobrecargas e declarações dentro de funções de teste. |
| Distribuição | Precisa melhorar | O cgo complica builds cruzados. O Windows agora roda os testes no CI, e cada binário de release passa por um teste rápido antes de ser publicado. |

**O que mais me preocupa.** Cerca de 8 das 17 mil linhas são regras de extração e resolução por
linguagem. Sem um type checker, cada padrão não coberto (generics pesados, callbacks tipados pelo
contexto, type guards, overloads) vira mais uma regra escrita à mão, e esse código vai crescer mais
rápido que o resto. O benchmark ainda é rodado por quem escreveu o Mira: cada ferramenta foi medida uma
vez em 107 a 320 símbolos por repositório, e a comparação com agentes é um piloto, com uma rodada do
Claude Haiku por célula. Arquivos grandes ainda custam cerca de um segundo por edição, porque as
palavras e referências deles são regravadas inteiras.

**Eu usaria?** Sim, como camada de navegação e edição de um agente em repositórios TypeScript, Go, Java
ou Python onde uma referência errada sai cara, com o grep ao lado para varreduras exaustivas de texto.
Não onde o recall precisa ser completo: aí um language server ou um índice apoiado em compilador ganha.
O que faria o projeto avançar: um fallback opcional com type checker para as referências que o Mira
deixa sem resolver, começando pelo TypeScript; um benchmark maior rodado por outra pessoa; e retorno de
quem não é autor do projeto.
