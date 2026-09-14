---
name: mira
description: Navigate this repository by symbol with mira (MCP tools or CLI) instead of reading whole files. Use when locating a definition, tracing calls, doing impact analysis, understanding what a function returns, throws or calls, or answering "where is X / who uses X".
---

# mira: navigate by symbol, read by line range

mira indexes TypeScript, JavaScript, Java, Python and Go symbols, references and imports
in `.mira/index.db`. Every answer is compact text. Code lines come numbered (`97| ...`):
cite them as `file:line` or `file:start-end` without counting. Nothing returns a whole file.

One capability, two front doors:

| MCP tool | CLI | answers |
|---|---|---|
| `list_files` | `mira files [path] --depth N --dirs-only --exclude-tests` | where things are (tree with symbol counts) |
| `resolve_symbol` | `mira resolve <name>... --include-skeleton --include-body --include-refs --kind K --path P --exclude-tests --depth N` | what X is, where, who calls it, what it calls; a class lists its members |
| `find_references` | `mira refs <name|Container.member>... --path P --kind K --exclude-tests --limit N` | every use of one or more names, with the calling method; `Container.member` keeps the uses resolved to it |
| `search_text` | `mira search <word> --path P --exclude-tests --regex --limit N --per-file N --context N` | identifiers, sub-tokens, strings, comments and text files; regex greps line text |
| `list_symbols` | `mira symbols <file> --compact` | outline of one file |
| `get_snippet` | `mira snippet <file> <start> <end>` / `mira snippet <file> --symbol <name>` | exact lines; the header names the enclosing symbol |
| `index_status` / `reindex` | `mira status` / `mira index` | freshness; the index refreshes itself before every query, so edits are visible at once |
| `replace_symbol_body`, `insert_before_symbol`, `insert_after_symbol` | `mira edit replace <file> <symbol> --text "..."`, `mira edit insert-before ...`, `mira edit insert-after ...` | edit by symbol: swap a whole definition or add text around it, no line counting; replace re-checks every caller |
| `replace_in_symbol` | `mira edit replace-in <file> <symbol> --old-text "..." --new-text "..."` | swap an exact snippet inside one symbol; must match once (`all` for every one); only the changed lines are rewritten |
| `rename_symbol` | `mira edit rename <file> <symbol> <new-name>` | rename at the declaration and every reference resolved to it, all files, one call |
| `delete_symbol` | `mira edit delete <file> <symbol>` (`--force`) | delete a definition and its doc comment; refuses while something still uses it |

MCP parameters and CLI flags have the same names: `exclude_tests` is `--exclude-tests`, `include_skeleton`
is `--include-skeleton`, `include_refs` is `--include-refs`, `old_text` is `--old-text`. In the CLI, `name`,
`word`, `file`, `start_line`, `end_line`, `new_name` and the `symbol` of an edit are positional arguments.

## Pick the tool from the question

- "Where is X defined?" → `resolve_symbol`. The name can be `Container.member`, a qualified
  Java name, a `file:line`, or several names separated by commas. An `export default` is named
  after its file (`resolve validateField`). Homonyms: filter with `kind`, `path`, `exclude_tests`
  (production definitions come first anyway). A class or interface longer than 30 lines
  answers with `members (n)`: its methods and fields with line ranges.
- "Who calls or uses X?" (impact analysis) → `find_references` with `exclude_tests` when only
  production matters. Several names go in one call (`name` = "addPet, addVisit" in MCP,
  `mira refs addPet addVisit` in the CLI). `Owner.addVisit` lists only the uses resolved
  to that definition and counts the ambiguous or unresolved ones with the same name; `Owner.getPet:126`
  keeps the overload that contains line 126. Each line has the kind, the target when
  several exist, the containing method with its range, and the source line. `resolve_symbol`
  with `include_refs` gives the definition and every use in one call.
- "What does X return, throw or call?" → `resolve_symbol` with `include_skeleton`: returns and
  throws with line numbers, locals, nested closures, and what it uses grouped by origin (same
  file `:line`, other files `file:line`, external packages, outer state). Then `get_snippet`
  only the lines the skeleton points to.
- "Show me the code of X" → `include_body` for short functions. For long ones (hundreds of
  lines) take `include_skeleton` first, then `around` with the line of the return or throw
  you care about (a window of ±8 lines by default, `context` changes it), or `get_snippet`.
- "Change a few lines of X" → `replace_in_symbol` with the exact lines (copied from `resolve_symbol`
  or `get_snippet`, indentation included). It costs the snippet, not the function: prefer it over
  `replace_symbol_body` whenever the function is long and the change is small.
- "Change X" → `resolve_symbol` to see the definition, then `replace_symbol_body` with the whole
  new definition (signature and body), or `insert_after_symbol` / `insert_before_symbol` to add
  code next to it. The answer already says whether callers still resolve and whether the file
  still parses: no follow-up query to verify.
- "Rename X" → `rename_symbol` once, not a search-and-replace. It touches only references resolved
  to X, so a homonym in another class survives, and it refuses a name already taken in that scope.
  Read its `skipped` list: unresolved uses that may be X, each with its line, left for you to
  decide; the `note` lines point at mentions in comments, strings and docs, and at lines using the
  name in syntax the index has no reference for. Overloads share a name: pass `name:line` (a line
  inside the one you mean). A TypeScript default export is renamed in its own file only; importers
  keep their local names. Add `preview` (`--preview`) to see every line first.
- "Remove X" → `delete_symbol`. If it refuses, the error lists who still uses X: update those,
  then delete. `force` deletes anyway and reports the dangling sites.
- "What is in this file or directory?" → `list_symbols` (`compact` for big files) or
  `list_files` with `depth`.
- "Where does the word or string 'xyz' appear?" → `search_text`. Every hit names the
  enclosing symbol; `context` adds lines around it. Marks: `D` definition line, `s` inside a
  string literal, `c` inside a comment, `t` in a text file (`.properties`, `.yml`, `.html`,
  `.sql`, `.md`, `.xml`, `.json`), `~` sub-token of a compound identifier (search `date` finds
  `visitDate`, `VISIT_DATE`). Use `regex` only for patterns: a plain word search is cheaper.
- Unknown repository → `list_files` with `dirs_only` once, then narrow with `path`.

## Recipes

Impact analysis (what breaks if I change `Owner.addVisit`):
1. `find_references` name=`addPet, addVisit` exclude_tests=true. `targets:` lists the
   definitions; `candidates:` means some refs are ambiguous and could be any of them
   (overloads are told apart by the number and, when known, the types of the arguments).
2. For each caller you must understand: `resolve_symbol` name=`Class.method` include_skeleton=true.

Trace a flow (how `register` reaches validation):
1. `resolve_symbol` name=`register` exclude_tests=true: definition, callers, callees and
   "referenced by" (who exposes it, e.g. a factory returning `{ register, handleSubmit }`).
2. On each hop `resolve_symbol` with `include_skeleton`: `uses (other files)` gives the next
   `file:line`; closures inside a factory are indexed as `Outer.inner` and listed under `nested`.
3. `get_snippet` only the ranges you still need.

Understand a function before editing it: `resolve_symbol` with `include_skeleton`, then
`include_body` or `get_snippet` for the part you will change.

## Reading the output

- Definition: `Name kind file:start-end [exported]`, annotations, signature, the whole numbered
  body when it has up to 30 lines (otherwise an 8-line snippet and a `… N more lines` hint),
  then `callers (n)` and `callees (n)` as `name kind file:start-end`.
- References: `line [kind resolution -> Target] in Container:start-end: source line`. The
  resolution shows only when it is not resolved: `ambiguous` (several candidates, none
  guessed), `external` (package outside the repository), `unresolved`.
- Skeleton: `N lines, N branches, N loops, N nested function(s), async`; `returns (n)` and
  `throws (n)` with lines; `locals` (a range for multi-line values); `nested (n)`;
  `uses (same file | other files | external)`; `outer` (variables, fields and closure state
  read from outside); `unresolved`. Callees are folded into `uses`. Chained calls
  (`owner.getPet(id).addVisit()`) and inherited members are followed through declared types.
- `truncated at N tokens (M lines omitted)`: the answer hit `max_tokens` (default 4000). Narrow
  with `path`, `kind` or `exclude_tests`, or raise `max_tokens`.

## Rules

- Never read a whole file to find something: `resolve_symbol`, `find_references` or
  `search_text` first, `get_snippet` on a range after.
- Cite `file:line` from the numbered output.
- Prefer `exclude_tests` unless tests are the question.
- `limit` caps total hits and `per_file` caps hits per file; a cut file shows `...` under it.
- Edits are picked up automatically: every query refreshes the index first (incremental,
  milliseconds). `reindex` is only needed when the server runs with `--no-auto-index`.
- A call identical to one already answered in this session returns `same as call #N`; pass
  `fresh=true` only if you really need the text again.
- Templates, SQL, properties, YAML, Markdown, XML and JSON are indexed by words only
  (`search_text`, mark `t`): no symbols, no references. Binary files and lockfiles are not indexed.
- `unresolved` and `ambiguous` are honest: the index never guesses. Follow `candidates:` or
  the import to decide.
