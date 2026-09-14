# Manual do Mira (CLI e MCP)

Referência completa dos comandos, flags e formatos de saída. A apresentação do projeto e os benchmarks estão no [README](../README.pt-BR.md).

`mira` indexa um repositório (TypeScript, JavaScript, Java, Python e Go) de forma
incremental e responde perguntas de navegação por símbolo — *o que é
`calculateDiscount`, onde é definida, quem a chama, o que ela chama* —
devolvendo **trechos por range de linhas, nunca arquivos inteiros**. O objetivo
é reduzir os tokens que agentes de IA gastam navegando código com `ls`, `grep`
e `cat`. A mesma API é exposta como servidor MCP (stdio).

O parser é o tree-sitter oficial via cgo (`github.com/tree-sitter/go-tree-sitter`
com as gramáticas typescript, tsx, java, python e go): precisa de um compilador C na
máquina de build. A primeira versão usava um runtime Go puro, que falhava em
um terço dos arquivos TypeScript reais; a troca está documentada em
[field-test-2026-09-14.md](field-test-2026-09-14.md).

## Instalação

Precisa de Go 1.26+ e de um compilador C (o runtime do tree-sitter é cgo:
clang no macOS, gcc no Linux, mingw no Windows).

```bash
go install github.com/JonathanSantos/mira/cmd/mira@latest   # binário em $(go env GOPATH)/bin
```

Alternativas: `make install` num clone; `brew tap JonathanSantos/mira
https://github.com/JonathanSantos/mira && brew install mira` (a fórmula
em `Formula/` compila do fonte); binários por SO/arquitetura saem em cada
tag `v*` pelo workflow de release. `mira version` mostra a versão.

## Como validar num projeto seu

```bash
cd ~/meu-projeto                       # TS/JS/Java/Python/Go; outros arquivos entram só por palavras
mira init                         # cria .mira/ (sugere .mira/ no .gitignore, não edita)
mira index                        # primeira indexação; depois é incremental
mira doctor                       # confere runtime, git, índice, .gitignore, skill e mostra a linha do MCP
mira skill --install              # .claude/skills/mira/SKILL.md: ensina o agente a usar bem
mira files --dirs-only            # primeira olhada
mira resolve NomeDeUmaClasse      # membros; resolve Classe.metodo --include-skeleton para a estrutura
mira refs nomeDeUmMetodo --exclude-tests
mira search "palavra da regra de negócio" --context 2
```

Ligando ao agente:

- **Claude Code**: `claude mcp add mira -- mira --repo /caminho/do/projeto mcp`
  (ou `--single-tool` para uma tool só). A skill instalada é carregada quando o
  assunto aparece; no MCP ela também é o resource `mira://skill` e o
  prompt `/mcp__mira__guide`.
- **Cursor, Windsurf, Cline e outros**: `{"mcpServers": {"mira": {"command":
  "mira", "args": ["--repo", "/caminho/do/projeto", "mcp"]}}}`.
- Depois de editar arquivos não é preciso fazer nada: toda consulta (CLI e MCP)
  roda uma indexação incremental antes de responder, em milissegundos quando
  nada mudou. `--no-auto-index` desliga isso; `mira status` mostra
  quantos arquivos estariam desatualizados.

Um roteiro de validação que funcionou nos testes de campo: peça ao agente
uma análise de impacto ("quem chama X, só produção"), um rastreamento de
fluxo ("de A até B, cite arquivo:linha") e uma explicação de função ("o que
Y devolve e o que pode lançar"), e compare com a mesma pergunta sem o MCP.
Os logs do agente (chamadas e bytes) são a medida; o gabarito é o código.

## Uso

```bash
cd meu-repo
mira init            # cria .mira/ (index.db + config.yaml); idempotente
mira index           # incremental; --full reindexa tudo
mira status
mira files --dirs-only                   # primeira olhada: só diretórios com totais
mira files src --depth 2 --exclude-tests # árvore dos arquivos indexados com contagem de símbolos
mira resolve calculateDiscount --depth 2
mira resolve register --include-refs --include-body --exclude-tests # definição, corpo numerado e usos numa chamada
mira resolve Field --kind type --path src/types                 # filtra homônimos
mira resolve validateField --include-skeleton --exclude-tests   # estrutura sem o corpo: retornos, throws, locais, usos por origem
mira refs formatMoney                    # inclui acessos a campo/propriedade, com o método chamador
mira refs addPet addVisit --exclude-tests # vários nomes numa chamada (JSON vira uma lista)
mira refs Owner.addVisit --exclude-tests  # só os usos resolvidos para o método; ambíguos e sem resolução são contados
mira resolve Owner.getPet Owner.addVisit # idem para resolve; uma classe longa lista os membros
mira search date --context 2             # sub-tokens (visitDate), strings, comentários, arquivos de texto, com contexto
mira doctor                              # o que falta para funcionar bem neste repositório
mira version
mira symbols src/pricing/discount.ts     # com anotações/decorators; --compact só nome, kind e linhas
mira snippet src/pricing/discount.ts 12 14
mira snippet src/pricing/discount.ts --symbol calculateDiscount
mira edit replace src/pricing/discount.ts calculateDiscount < nova.ts        # troca a definição e confere quem chama
mira edit replace-in src/pricing/discount.ts calculateDiscount --old-text 'roundMoney(' --new-text 'Math.round(' # só o trecho, sem reenviar a função
mira edit rename src/pricing/discount.ts calculateDiscount computeDiscount --preview   # todas as linhas que mudariam
mira edit rename src/pricing/discount.ts calculateDiscount computeDiscount   # declaração e refs resolvidas, todos os arquivos
mira edit delete src/pricing/discount.ts applyCoupon    # recusa enquanto algo usa; --force apaga e lista o que ficou pendurado
mira search telephone --path src/main --exclude-tests # identificadores e palavras em strings, definição primeiro
mira search 'TODO|FIXME' --regex                    # grep sobre o texto das linhas
mira skill           # imprime a skill que ensina um agente a usar o mira
mira skill --install # grava .claude/skills/mira/SKILL.md no repositório (nunca sobrescreve sem --force)
mira mcp             # servidor MCP em stdio (--single-tool: uma tool `explore` com `action`)
```

As flags têm os nomes dos parâmetros MCP em kebab-case (`exclude_tests` é
`--exclude-tests`, `include_refs` é `--include-refs`): um agente usa o mesmo
vocabulário nos dois lados. Os nomes antigos (`--no-tests`, `--skeleton`,
`--with-refs`, `--old`, `--new`) continuam aceitos.

`edit` trabalha por símbolo, sem contar linhas, e sempre reindexa e confere o
resultado. `replace` diz quantas referências continuam resolvendo, quais
quebraram e se o arquivo deixou de parsear. `replace-in` troca um trecho exato
dentro do símbolo (precisa aparecer uma vez, ou `--all`) e reescreve só as
linhas que mudam: numa função de 200 linhas o agente manda a linha, não a
função. `rename` troca o nome só na
declaração e nas referências resolvidas para aquele símbolo, em todos os
arquivos: um método homônimo em outra classe fica intocado, usos `unresolved`
que talvez sejam ele voltam listados, e menções em comentários e strings são
apontadas sem mudar. `delete` leva junto o comentário de documentação e recusa
enquanto houver referências ao símbolo ou a membros dele, dizendo onde.
`--preview` mostra o diff sem gravar nada. Sobrecargas dividem o nome: `Owner.getPet:126`
escolhe a que contém a linha, e um nome ambíguo nunca vira o primeiro da lista.
Um export default de TypeScript é renomeado só no próprio arquivo (quem importa
usa um nome local, que continua válido), como faz o tsserver.

`search` agrupa por arquivo, põe primeiro os arquivos onde o nome é definido
(`D`), marca palavras encontradas dentro de literais de string (`s`) e aceita
`--path` (prefixo ou glob com `**`), `--exclude-tests`, `--limit` e `--per-file`.
`refs` aceita vários nomes, `--path`, `--kind`, `--exclude-tests` e `--limit` (por
nome), e cada ref diz o método que a contém com o range dele
(`in Owner.addVisit:173-183`). Um nome `Container.member` (`refs Owner.addVisit`)
lista só as referências resolvidas para aquela definição (`Owner.getPet:126` fica
com a sobrecarga que contém a linha) e conta, numa linha no fim, as ambíguas ou
sem resolução com o mesmo nome. `resolve` aceita `--kind`, `--path` e `--exclude-tests`, põe
definições de produção antes das de teste, lista quem expõe o símbolo sem
chamá-lo (`referenced by`) e, com `--include-refs`, traz os usos por arquivo na
mesma chamada. Definições de até 30 linhas vêm com o corpo inteiro sem
`--include-body`; acima disso, uma prévia de 8 linhas e a dica
`… N more lines: include-body, skeleton, or snippet início-fim`. `resolve --include-skeleton` (MCP `include_skeleton`) troca o corpo
pela estrutura da função: contagens (linhas, ramos, laços, funções aninhadas,
async), cada `return` e `throw` com a linha, `throws` declarado (Java), locais,
closures aninhadas indexadas e o que a função usa agrupado pela origem do alvo
(mesmo arquivo `:linha`, outros arquivos `arquivo:linha`, pacotes externos,
estado de fora, não resolvidos). Retornos, throws e locais consideram só o
escopo próprio (o `return` de um callback não é o da função); ramos e laços
contam o corpo inteiro. A estrutura vem de um reparse do arquivo na hora da
consulta, os usos vêm do índice; se o arquivo mudou desde o último `index`,
o esqueleto avisa em vez de chutar. Snippets e corpos saem com número de linha
(`--no-numbers` desliga). `files` colapsa cadeias de diretório único e mostra a cadeia
inteira (`src/main/java/com/acme/`). Funções aninhadas em funções (closures
de uma factory) são símbolos `Externa.interna`; um `export default` anônimo
recebe o nome do arquivo.

Todos os comandos aceitam `--repo <path>` (default: cwd, subindo até achar
`.mira/` ou `.git/`), `--json` para saída estruturada, `--max-tokens N`
para cortar a saída de texto no orçamento, com aviso do que ficou de fora, e
`--no-auto-index` para consultar sem antes atualizar o índice. O
modo texto é o mesmo formato compacto que o MCP devolve: uma linha por item,
caminho uma vez por grupo, itens relacionados como `nome kind arquivo:início-fim`.
Sugerimos adicionar `.mira/` ao `.gitignore` do repositório alvo — o
`init` avisa, mas nunca edita o arquivo.

`resolve` aceita nome simples (`total`), `Container.membro` (`OrderService.total`),
nome Java qualificado (`com.acme.pricing.Discount.calculate`) e `arquivo:linha`.

### Configuração (`.mira/config.yaml`)

```yaml
languages: [typescript, javascript, java, python, go]   # vazio = todas
ignore:                                     # padrões extras, estilo .gitignore
  - "**/generated/**"
```

## Exemplos de saída

O formato de texto abaixo é o que a CLI imprime e o que o MCP devolve.

### Front-end React (`testdata/fixtures/ts-frontend`)

`<Button />` em `Checkout.tsx` chega ao `export default` de `Button.tsx`
atravessando dois barrels (`ui.ts` → `components/index.ts`):

```
$ mira resolve Button
Button function src/components/Button.tsx:10-18 [exported jsx]
  export default function Button({ label, price, onClick }: ButtonProps)
  10| export default function Button({ label, price, onClick }: ButtonProps) {
  11|   const suffix = price === undefined ? '' : ` (${formatMoney(price)})`;
  12|   return (
  ...
  callers (1):
    Checkout function src/pages/Checkout.tsx:6-15
  callees (1):
    formatMoney function src/utils/format.ts:3-5

$ mira refs formatMoney
refs formatMoney: 5 total (ambiguous 1, resolved 4) by kind (call 3, import 2)
targets:
  formatMoney function src/utils/format.ts:3-5
candidates (ambiguous refs could be any of these):
  formatMoney function src/legacy/format.ts:2-4
  formatMoney function src/utils/format.ts:3-5
src/components/Button.tsx (2)
  2 [import]: import { formatMoney } from '../utils/format';
  11 [call] in Button:10-18: const suffix = price === undefined ? '' : ` (${formatMoney(price)})`;
src/hooks/useCart.ts (2)
  2 [import]: import { formatMoney } from '../utils/format';
  17 [call] in useCart:9-18: return { items, add, total, label: formatMoney(total) };
src/legacy/widget.js (1)
  4 [call ambiguous] in renderPrice:3-5: el.textContent = formatMoney(value);
```

Imports de `.css`/`.svg` ficam `external` sem erro. O script legado usa
`formatMoney` sem import e há duas definições exportadas: a ref fica
`ambiguous` com os candidatos listados — nunca chutamos.

### Back-end Node (`testdata/fixtures/node-backend`)

NestJS-like com decorators e injeção por construtor, mais módulos CommonJS:

```
$ mira resolve total
OrderController.total method src/orders/order.controller.ts:8-15 [exported]
  @Get(':id/total')
  total(@Param('id') id: string): number
   8|   @Get(':id/total')
   9|   total(@Param('id') id: string): number {
  10|     const order = this.orders.findOne(id);
  ...
  callers (0):
  callees (2):
    OrderService.findOne method src/orders/order.service.ts:20-22
    OrderService.total method src/orders/order.service.ts:24-27

OrderService.total method src/orders/order.service.ts:24-27 [exported]
  total(order: Order): number
  ...
  callers (1):
    OrderController.total method src/orders/order.controller.ts:8-15
  callees (1):
    calculateDiscount function src/pricing/discount.ts:12-14

$ mira symbols src/orders/order.service.ts
src/orders/order.service.ts (6 symbols)
4-8 interface OrderItem [exported]  export interface OrderItem
10-14 interface Order [exported]  export interface Order
16-28 class OrderService [exported]  @Injectable()  export class OrderService
  18-18 property orders  private readonly orders
  20-22 method findOne [exported]  findOne(id: string): Order | undefined
  24-27 method total [exported]  total(order: Order): number
```

`this.orders.total()` resolve porque `orders: OrderService` está declarado no
construtor. `const { calculateTax } = require('./tax')` resolve para o
`module.exports = { calculateTax }`. `@Injectable` vem de `@nestjs/common` e
fica `external`.

### Java (`testdata/fixtures/java-service`)

```
$ mira resolve com.acme.pricing.Discount.calculate
com.acme.pricing.Discount.calculate method src/main/java/com/acme/pricing/Discount.java:6-11 [exported]
  public static Money calculate(Money base, int percent)
  ...
  callers (2):
    com.acme.order.DefaultOrderService.total method src/main/java/com/acme/order/DefaultOrderService.java:19-33
    com.acme.report.ReportService.discounted method src/main/java/com/acme/report/ReportService.java:11-13
  callees (3):
    com.acme.pricing.Money record src/main/java/com/acme/pricing/Money.java:3-13
    com.acme.pricing.Money.cents field src/main/java/com/acme/pricing/Money.java:3-3
    com.acme.pricing.Money.percent method src/main/java/com/acme/pricing/Money.java:10-12

$ mira resolve OrderService --depth 0
com.acme.order.OrderService interface src/main/java/com/acme/order/OrderService.java:5-7 [exported]
  public interface OrderService
  ...
  implemented by:
    com.acme.order.DefaultOrderService class src/main/java/com/acme/order/DefaultOrderService.java:7-46
```

`ReportService.discounted` chega a `calculate` via `import static`;
`OrderController` usa `OrderService` da mesma package sem import e `Money` via
`import com.acme.pricing.*`; `java.util.List` é `external`; `LegacyBridge`
importa dois wildcards que definem `Money` e fica `ambiguous`.

## Servidor MCP

```bash
mira mcp
```

Tools: `list_files`, `resolve_symbol` (com `include_skeleton`, `include_body`,
`include_refs`), `find_references`, `search_text`, `list_symbols`,
`get_snippet`, `index_status`, `reindex`, e as de edição
`replace_symbol_body`, `insert_before_symbol`, `insert_after_symbol`,
`replace_in_symbol`, `rename_symbol` e `delete_symbol` (todas com `preview`;
`delete_symbol` com `force`). `rename_symbol` recusa um nome já declarado no
mesmo escopo e lista, com a linha, os usos não resolvidos que talvez sejam o
símbolo; um preview grande mostra três mudanças por arquivo. Resource `mira://skill` e prompt
`guide` entregam a skill (seção acima). As descrições (e as `instructions` do
servidor) orientam o agente a começar por `list_files`, chamar `resolve_symbol`
antes de ler arquivos e pedir ranges (ou um símbolo) com `get_snippet`. As respostas são só texto compacto (sem `structuredContent`,
que duplicava cada resposta no fio) e toda tool aceita `max_tokens`
(padrão 4000): o texto é cortado no fim de uma linha com uma dica de como
estreitar. Medido no react-hook-form, em bytes no fio: `find_references` de
104 KB para 3,7 KB, `list_symbols` de 41 KB para 7,1 KB, `resolve_symbol` de
8,2 KB para 1,5 KB.

Exemplo de configuração para um cliente MCP:

```json
{ "mcpServers": { "mira": { "command": "mira", "args": ["--repo", "/caminho/do/repo", "mcp"] } } }
```

## Como funciona

```
walk (git ls-files ou walk manual com .gitignore)
  → fingerprint (size+mtime) mudou?  não → pula
  → sha256 mudou?                    não → atualiza fingerprint e pula
  → workers em paralelo: scanner lexical + parse (tree-sitter) + extractor
  → um único escritor SQLite (WAL), lote por transação
  → remove arquivos que sumiram
  → resolve refs/imports dos arquivos afetados e gera edges
```

Pacotes (`internal/`): `walker`, `hasher`, `scanner`, `parser`, `extract`
(+ `typescript`, `java`), `store`, `resolve`, `indexer` (lado de escrita),
`graph` (lado de leitura), `cli` e `mcp` (adaptadores finos sobre os dois).

Em TS uma função sem anotação de retorno ganha um tipo inferido do primeiro
`return` (`new X()`, campo tipado, `f()` um nível), e `const x = f()` segue
o retorno de `f`; em Java e TS, `List<X>`/`X[]`/`Optional<X>` entregam X em
`get()`, `orElseThrow()`, `find()` e afins.

Um membro que não está no tipo do receiver é procurado nos tipos pai
(`extends`/`implements`) dentro do índice, subindo até 8 níveis; o pai é
resolvido no contexto do arquivo dele na hora, porque esse arquivo pode ainda
não ter sido resolvido na rodada. Quando um pai está fora do índice
(`JpaRepository.save`), o membro é `external` em vez de `unresolved`.
Sobrecargas se separam pelo número de argumentos da chamada e pelo tipo de
cada um quando o extractor consegue saber (literal, variável ou campo com
tipo declarado, `new T()`, cast); o que não se separa fica `ambiguous`.

### Tempos medidos (Apple Silicon, fixtures de 9 arquivos)

| fixture | `index --full` | `index` sem mudanças |
|---|---|---|
| ts-frontend | ~21 ms (parse+extract 17 ms, resolve 3,6 ms) | ~1,2 ms |
| node-backend | ~20 ms (parse+extract 16 ms, resolve 3,4 ms) | ~1,4 ms |
| java-service | ~15 ms (parse+extract 10 ms, resolve 4,9 ms) | ~1,5 ms |

```
$ mira resolve DefaultOrderService.total --include-skeleton
com.acme.order.DefaultOrderService.total method src/main/java/com/acme/order/DefaultOrderService.java:19-33 [exported]
  @Override
  public Money total(Order order)
  ...
  callers (1):
    com.acme.order.OrderController.show method src/main/java/com/acme/order/OrderController.java:13-15
  skeleton: 15 lines, 1 branches, 1 loops
  returns (2):
    23 return cached;
    32 return result;
  locals: cached (21), sum (25), lines (26), result (30)
  uses (same file):
    Cache.get :38 (21)
    Cache.put :42 (31)
  uses (other files):
    Order.id src/main/java/com/acme/order/Order.java:15 (21, 31)
    Order.lines src/main/java/com/acme/order/Order.java:19 (26)
    Discount.calculate src/main/java/com/acme/pricing/Discount.java:6 (30)
    Money.plus src/main/java/com/acme/pricing/Money.java:6 (28)
  outer:
    Money.ZERO property src/main/java/com/acme/pricing/Money.java:4 (25)
    DefaultOrderService.percent property :10 (30)
    DefaultOrderService.cache field :12 (21, 31)
```

Com `--include-skeleton` os `callees` somem: os `uses` já os cobrem, com a linha
de cada uso entre parênteses.

## Skill para o agente

A skill é um markdown embutido no binário que ensina qual tool responde qual
pergunta, receitas (impacto, rastreamento, entender uma função antes de
editar), como ler a saída e quando **não** usar o mira (templates, SQL,
properties e YAML não são indexados). Uma fonte só, servida em três lugares:

- `mira skill` imprime; `mira skill --install` grava
  `.claude/skills/mira/SKILL.md` no repositório alvo (`--dir` muda o
  destino, `--force` sobrescreve). É opt-in e nunca toca um arquivo existente
  sem `--force`, pela mesma regra do `.gitignore`.
- No MCP, o resource `mira://skill` e o prompt `guide` (no Claude Code,
  `/mcp__mira__guide`). As `instructions` do servidor são a versão curta e
  apontam para os dois.

Por que skill e não instruções longas no servidor: as `instructions` entram no
system prompt em toda rodada; uma skill no workspace carrega só a descrição
sempre e o corpo quando o assunto aparece. E por que não uma tool
`install_skill`: o schema de cada tool é pago em todo turno, e instalar é ação
única. Dois testes mantêm a skill honesta: todo nome com underscore entre
crases tem de ser uma tool ou um parâmetro do servidor, e todo
`mira <cmd> --flag` citado tem de existir na CLI.

## Limitações conhecidas (v0)

- **Rename e delete confiam no índice**: só mudam referências resolvidas para
  o símbolo. Reflexão, nomes dentro de strings (`@Value`, templates,
  `getattr`) e usos `unresolved` ficam de fora e voltam listados; o arquivo
  Java não é renomeado junto com a classe (a resposta avisa). Todos os
  arquivos são calculados antes de gravar o primeiro, mas a gravação em si não
  é atômica: uma falha de disco no meio deixa os anteriores gravados.

- **Java sem inferência de tipos**: `obj.m()` resolve só quando o tipo de
  `obj` é lido de uma declaração no mesmo arquivo (parâmetro, local, campo,
  `new T()`); chamadas encadeadas (`a.b().c()`) ficam `unresolved`. Membros
  herdados resolvem subindo `extends`/`implements` dentro do índice (um pai
  fora do índice torna o membro `external`). Sobrecargas se separam pelo
  número de argumentos e pelo tipo de cada um quando se conhece (literal,
  variável com tipo declarado, `new T()`, com boxing `int`/`Integer`); o
  que sobrar fica `ambiguous`, nunca chutado. Uma subclasse passada a um
  parâmetro da superclasse não separa a sobrecarga.
- **TS/JS**: `tsconfig.json` `paths`/`baseUrl` não são lidos (ponto marcado em
  `resolve/typescript.go`); `varTypes` não tem escopo (a última declaração
  vence); refs a namespaces (`ns.x`) resolvem por nome exportado, sem tipos.
- **Scanner lexical**: literais regex não são reconhecidos (podem gerar
  palavras espúrias); palavras em comentários ficam fora do índice de
  identificadores (use `search --regex`); palavras em strings entram com a
  marca `in_string`.
- **Incremental**: uma definição nova em outro arquivo não transforma uma ref
  `unresolved` em `ambiguous` até um `index --full` (refs já `ambiguous` são
  re-resolvidas a cada rodada).
- **Parser**: `.ts`/`.mts`/`.cts` usam a gramática typescript; `.tsx`, `.js`,
  `.jsx`, `.mjs` e `.cjs` usam a tsx (JSX); `.py`/`.pyi` a de Python e `.go`
  a de Go. Arquivos com erro de parse são indexados com a árvore parcial e
  marcados com `parse_error`.
- **Go**: o pacote é o diretório; imports do próprio módulo (lidos de
  `go.mod`) resolvem, os demais são `external`; struct embutida promove
  métodos como herança; `x := f()` segue o retorno de `f`, `new(T)`,
  `make([]T, n)` e `for _, v := range xs` (com `xs` de tipo conhecido) dão
  tipo à variável; a chave de um literal composto (`Cmd{Use: "x"}`) é o
  campo do tipo, então `refs Use` lista quem o preenche; `type A = B`
  conta como tipo. Sem genéricos além do nome base, sem interfaces
  implícitas (quem implementa uma interface não aparece em
  `implemented by`).
- **Python**: imports relativos e absolutos (raiz, `src/` e os diretórios
  acima do arquivo) e re-exports em `__init__.py`; tipos vêm de anotações,
  de `x = T()` e de `x = f()` com `-> T`; `self.attr` tipado no `__init__`;
  bases de classe viram herança. Sem inferência além disso, sem `typing`
  avançado (Protocol, TypeVar).

## Desenvolvimento

```bash
gofmt -l . && go vet ./... && golangci-lint run ./... && go test -race ./...
```
