// Gabarito TypeScript: definições e referências pelo type checker oficial.
// Uso: node refs.mjs <raiz do repositório> <diretório de saída>
// Grava defs.jsonl e refs.jsonl no mesmo formato do gotruth.
import fs from 'node:fs';
import path from 'node:path';
import ts from 'typescript';

const [rootArg, outArg] = process.argv.slice(2);
if (!rootArg || !outArg) {
  console.error('usage: node refs.mjs <repo root> <out dir>');
  process.exit(2);
}
const root = path.resolve(rootArg);
const out = path.resolve(outArg);
const skipDirs = new Set(['node_modules', '.git', 'dist', 'build', 'coverage', '.next', 'out', '.mira', '.serena', '.turbo']);

function sourceFiles(dir, files = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!skipDirs.has(entry.name)) sourceFiles(full, files);
    } else if (/\.(ts|tsx|mts|cts)$/.test(entry.name) && !entry.name.endsWith('.d.ts')) {
      files.push(full);
    }
  }
  return files;
}

// Opções do tsconfig da raiz (paths, jsx, baseUrl), sem nada que restrinja a
// lista de arquivos: o programa inclui todo o código do repositório.
function compilerOptions() {
  const defaults = {
    jsx: ts.JsxEmit.Preserve, target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext,
    moduleResolution: ts.ModuleResolutionKind.Bundler, esModuleInterop: true, resolveJsonModule: true,
  };
  const configPath = path.join(root, 'tsconfig.json');
  let options = defaults;
  if (fs.existsSync(configPath)) {
    const { config } = ts.readConfigFile(configPath, ts.sys.readFile);
    options = { ...defaults, ...ts.parseJsonConfigFileContent(config ?? {}, ts.sys, root).options };
  }
  return { ...options, noEmit: true, skipLibCheck: true, allowJs: false, composite: false, incremental: false, declaration: false, rootDir: undefined };
}

const rel = (file) => path.relative(root, file).split(path.sep).join('/');
const inRepo = (file) => file.startsWith(root + path.sep) && !file.split(path.sep).includes('node_modules');

function isTopLevelVariable(decl) {
  const statement = decl.parent?.parent;
  return Boolean(statement && ts.isVariableStatement(statement) && ts.isSourceFile(statement.parent));
}

// kindOf devolve o tipo das declarações que as ferramentas tratam como
// símbolo; locais, parâmetros e chaves de objeto literal ficam de fora.
function kindOf(decl) {
  switch (decl.kind) {
    case ts.SyntaxKind.FunctionDeclaration: return 'function';
    case ts.SyntaxKind.ClassDeclaration: return 'class';
    case ts.SyntaxKind.InterfaceDeclaration: return 'interface';
    case ts.SyntaxKind.TypeAliasDeclaration: return 'type';
    case ts.SyntaxKind.EnumDeclaration: return 'enum';
    case ts.SyntaxKind.EnumMember: return 'enum_member';
    case ts.SyntaxKind.MethodDeclaration:
    case ts.SyntaxKind.MethodSignature: return 'method';
    case ts.SyntaxKind.PropertyDeclaration:
    case ts.SyntaxKind.PropertySignature: return 'property';
    case ts.SyntaxKind.VariableDeclaration: return isTopLevelVariable(decl) ? 'variable' : '';
    default: return '';
  }
}

function containerOf(decl) {
  const parent = decl.parent;
  if (parent && (ts.isClassLike(parent) || ts.isInterfaceDeclaration(parent) || ts.isEnumDeclaration(parent)) && parent.name) {
    return parent.name.text;
  }
  return undefined;
}

const program = ts.createProgram(sourceFiles(root), compilerOptions());
const checker = program.getTypeChecker();
const defs = new Map();
const refs = new Set();

function position(sf, node) {
  const { line, character } = sf.getLineAndCharacterOfPosition(node.getStart(sf));
  return { file: rel(sf.fileName), line: line + 1, col: character + 1 };
}

function definitionOf(symbol) {
  for (const decl of symbol.declarations ?? []) {
    const kind = kindOf(decl);
    const name = ts.getNameOfDeclaration(decl);
    if (!kind || !name || !inRepo(decl.getSourceFile().fileName)) continue;
    const pos = position(decl.getSourceFile(), name);
    const id = `${pos.file}:${pos.line}:${pos.col}`;
    if (!defs.has(id)) {
      defs.set(id, { id, name: name.getText(decl.getSourceFile()), kind, container: containerOf(decl), ...pos });
    }
    return { id, declarations: symbol.declarations };
  }
  return undefined;
}

function visit(sf, node) {
  if (ts.isIdentifier(node)) {
    let symbol = checker.getSymbolAtLocation(node);
    if (symbol && ts.isShorthandPropertyAssignment(node.parent) && node.parent.name === node) {
      symbol = checker.getShorthandAssignmentValueSymbol(node.parent) ?? symbol;
    }
    if (symbol && symbol.flags & ts.SymbolFlags.Alias) {
      symbol = checker.getAliasedSymbol(symbol);
    }
    const def = symbol && definitionOf(symbol);
    if (def && !def.declarations.some((d) => ts.getNameOfDeclaration(d) === node)) {
      const pos = position(sf, node);
      refs.add(JSON.stringify({ def: def.id, file: pos.file, line: pos.line, col: pos.col }));
    }
  }
  ts.forEachChild(node, (child) => visit(sf, child));
}

for (const sf of program.getSourceFiles()) {
  if (inRepo(sf.fileName)) visit(sf, sf);
}

fs.mkdirSync(out, { recursive: true });
const sortedDefs = [...defs.values()].sort((a, b) => a.id.localeCompare(b.id));
fs.writeFileSync(path.join(out, 'defs.jsonl'), sortedDefs.map((d) => JSON.stringify(d)).join('\n') + '\n');
fs.writeFileSync(path.join(out, 'refs.jsonl'), [...refs].sort().join('\n') + '\n');
console.log(`tstruth: ${sortedDefs.length} definitions, ${refs.size} references`);
