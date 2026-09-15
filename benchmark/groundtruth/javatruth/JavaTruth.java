import com.sun.source.tree.ClassTree;
import com.sun.source.tree.CompilationUnitTree;
import com.sun.source.tree.IdentifierTree;
import com.sun.source.tree.LineMap;
import com.sun.source.tree.MemberReferenceTree;
import com.sun.source.tree.MemberSelectTree;
import com.sun.source.tree.MethodTree;
import com.sun.source.tree.Tree;
import com.sun.source.tree.VariableTree;
import com.sun.source.util.JavacTask;
import com.sun.source.util.SourcePositions;
import com.sun.source.util.TreePath;
import com.sun.source.util.TreePathScanner;
import com.sun.source.util.Trees;

import java.io.IOException;
import java.io.PrintWriter;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;
import java.util.stream.Stream;
import javax.lang.model.element.Element;
import javax.tools.DiagnosticCollector;
import javax.tools.JavaCompiler;
import javax.tools.JavaFileObject;
import javax.tools.StandardJavaFileManager;
import javax.tools.ToolProvider;

/**
 * Gabarito Java do benchmark: definições e referências pelo javac (com.sun.source).
 * Sem o classpath das dependências o javac reporta erros de pacote ausente, mas
 * resolve tudo que é declarado no repositório, que é o que interessa aqui.
 *
 * Uso: java JavaTruth.java <raiz do repositório> <diretório de saída>
 */
public final class JavaTruth {
    record Def(String id, String name, String kind, String container, String file, long line, long col) {}

    record Ref(String def, String file, long line, long col) {}

    private final Path root;
    private final Trees trees;
    private final SourcePositions positions;
    private final Map<Element, Def> defs = new LinkedHashMap<>();
    private final Set<Ref> refs = new TreeSet<>(Comparator.comparing(Ref::def).thenComparing(Ref::file)
            .thenComparingLong(Ref::line).thenComparingLong(Ref::col));
    private final Map<CompilationUnitTree, CharSequence> sources = new HashMap<>();

    private JavaTruth(Path root, Trees trees) {
        this.root = root;
        this.trees = trees;
        this.positions = trees.getSourcePositions();
    }

    public static void main(String[] args) throws IOException {
        if (args.length != 2) {
            System.err.println("usage: java JavaTruth.java <repo root> <out dir>");
            System.exit(2);
        }
        Path root = Path.of(args[0]).toAbsolutePath().normalize();
        List<Path> files;
        try (Stream<Path> walk = Files.walk(root)) {
            files = walk.filter(p -> p.toString().endsWith(".java"))
                    .filter(p -> !p.toString().contains("/.git/") && !p.toString().contains("/node_modules/"))
                    .sorted().toList();
        }
        JavaCompiler compiler = ToolProvider.getSystemJavaCompiler();
        DiagnosticCollector<JavaFileObject> diagnostics = new DiagnosticCollector<>();
        StandardJavaFileManager fileManager = compiler.getStandardFileManager(diagnostics, Locale.ROOT, StandardCharsets.UTF_8);
        JavacTask task = (JavacTask) compiler.getTask(null, fileManager, diagnostics,
                List.of("-proc:none", "-implicit:none", "-nowarn"), null, fileManager.getJavaFileObjectsFromPaths(files));
        List<CompilationUnitTree> units = new ArrayList<>();
        task.parse().forEach(units::add);
        task.analyze();
        JavaTruth truth = new JavaTruth(root, Trees.instance(task));
        units.forEach(truth::collectDefinitions);
        units.forEach(truth::collectReferences);
        truth.write(Path.of(args[1]), diagnostics.getDiagnostics().size());
    }

    private void collectDefinitions(CompilationUnitTree unit) {
        new TreePathScanner<Void, Void>() {
            @Override
            public Void visitClass(ClassTree node, Void unused) {
                define(unit, getCurrentPath(), node.getSimpleName().toString());
                return super.visitClass(node, unused);
            }

            @Override
            public Void visitMethod(MethodTree node, Void unused) {
                define(unit, getCurrentPath(), node.getName().toString());
                return super.visitMethod(node, unused);
            }

            @Override
            public Void visitVariable(VariableTree node, Void unused) {
                define(unit, getCurrentPath(), node.getName().toString());
                return super.visitVariable(node, unused);
            }
        }.scan(unit, null);
    }

    private void define(CompilationUnitTree unit, TreePath path, String name) {
        Element element = trees.getElement(path);
        String kind = element == null ? null : kindOf(element);
        if (kind == null) {
            return;
        }
        if (name.equals("<init>")) {
            name = element.getEnclosingElement().getSimpleName().toString();
        }
        if (name.isEmpty()) {
            return; // classe anônima
        }
        long offset = nameOffset(unit, path.getLeaf(), name);
        if (offset < 0) {
            return;
        }
        LineMap lines = unit.getLineMap();
        long line = lines.getLineNumber(offset);
        long col = lines.getColumnNumber(offset);
        String file = relative(unit);
        Element enclosing = element.getEnclosingElement();
        String container = enclosing != null && (enclosing.getKind().isClass() || enclosing.getKind().isInterface())
                ? enclosing.getSimpleName().toString() : null;
        defs.put(element, new Def(file + ":" + line + ":" + col, name, kind, container, file, line, col));
    }

    private static String kindOf(Element element) {
        return switch (element.getKind()) {
            case CLASS -> "class";
            case INTERFACE, ANNOTATION_TYPE -> "interface";
            case ENUM -> "enum";
            case RECORD -> "record";
            case METHOD -> "method";
            case CONSTRUCTOR -> "constructor";
            case FIELD -> "field";
            case ENUM_CONSTANT -> "enum_member";
            default -> null; // locais, parâmetros e componentes de record não entram
        };
    }

    // nameOffset acha o nome depois dos modificadores e do tipo: a posição da
    // árvore começa nas anotações.
    private long nameOffset(CompilationUnitTree unit, Tree tree, String name) {
        long from = positions.getStartPosition(unit, tree);
        if (tree instanceof ClassTree c) {
            from = Math.max(from, positions.getEndPosition(unit, c.getModifiers()));
        } else if (tree instanceof MethodTree m) {
            from = Math.max(from, positions.getEndPosition(unit, m.getModifiers()));
            if (m.getReturnType() != null) {
                from = Math.max(from, positions.getEndPosition(unit, m.getReturnType()));
            }
        } else if (tree instanceof VariableTree v) {
            from = Math.max(from, positions.getEndPosition(unit, v.getModifiers()));
            if (v.getType() != null) {
                from = Math.max(from, positions.getEndPosition(unit, v.getType()));
            }
        }
        return indexOfWord(source(unit), name, (int) Math.max(from, 0));
    }

    private void collectReferences(CompilationUnitTree unit) {
        new TreePathScanner<Void, Void>() {
            @Override
            public Void visitIdentifier(IdentifierTree node, Void unused) {
                refer(unit, getCurrentPath(), positions.getStartPosition(unit, node));
                return super.visitIdentifier(node, unused);
            }

            @Override
            public Void visitMemberSelect(MemberSelectTree node, Void unused) {
                long end = positions.getEndPosition(unit, node);
                refer(unit, getCurrentPath(), end < 0 ? -1 : end - node.getIdentifier().length());
                return super.visitMemberSelect(node, unused);
            }

            // `NamedEntity::getName` usa o método. `Foo::new` fica de fora: o nome
            // na árvore é <init>, não o que está escrito.
            @Override
            public Void visitMemberReference(MemberReferenceTree node, Void unused) {
                if (node.getMode() != MemberReferenceTree.ReferenceMode.NEW) {
                    long end = positions.getEndPosition(unit, node);
                    refer(unit, getCurrentPath(), end < 0 ? -1 : end - node.getName().length());
                }
                return super.visitMemberReference(node, unused);
            }
        }.scan(unit, null);
    }

    private void refer(CompilationUnitTree unit, TreePath path, long offset) {
        if (offset < 0) {
            return;
        }
        Element element = trees.getElement(path);
        Def def = element == null ? null : defs.get(element);
        if (def == null) {
            return;
        }
        LineMap lines = unit.getLineMap();
        long line = lines.getLineNumber(offset);
        long col = lines.getColumnNumber(offset);
        String file = relative(unit);
        if (file.equals(def.file()) && line == def.line() && col == def.col()) {
            return;
        }
        refs.add(new Ref(def.id(), file, line, col));
    }

    private CharSequence source(CompilationUnitTree unit) {
        return sources.computeIfAbsent(unit, u -> {
            try {
                return u.getSourceFile().getCharContent(true);
            } catch (IOException e) {
                throw new IllegalStateException("reading " + u.getSourceFile().getName(), e);
            }
        });
    }

    private static int indexOfWord(CharSequence text, String word, int from) {
        String s = text.toString();
        for (int i = s.indexOf(word, from); i >= 0; i = s.indexOf(word, i + 1)) {
            boolean left = i == 0 || !Character.isJavaIdentifierPart(s.charAt(i - 1));
            int end = i + word.length();
            boolean right = end >= s.length() || !Character.isJavaIdentifierPart(s.charAt(end));
            if (left && right) {
                return i;
            }
        }
        return -1;
    }

    private String relative(CompilationUnitTree unit) {
        return root.relativize(Path.of(unit.getSourceFile().toUri()).normalize()).toString().replace('\\', '/');
    }

    private void write(Path out, int diagnostics) throws IOException {
        Files.createDirectories(out);
        List<Def> sorted = new ArrayList<>(defs.values());
        sorted.sort(Comparator.comparing(Def::id));
        try (PrintWriter w = new PrintWriter(Files.newBufferedWriter(out.resolve("defs.jsonl")))) {
            for (Def d : sorted) {
                w.printf("{\"id\":%s,\"name\":%s,\"kind\":%s,\"container\":%s,\"file\":%s,\"line\":%d,\"col\":%d}%n",
                        json(d.id()), json(d.name()), json(d.kind()), json(d.container()), json(d.file()), d.line(), d.col());
            }
        }
        try (PrintWriter w = new PrintWriter(Files.newBufferedWriter(out.resolve("refs.jsonl")))) {
            for (Ref r : refs) {
                w.printf("{\"def\":%s,\"file\":%s,\"line\":%d,\"col\":%d}%n", json(r.def()), json(r.file()), r.line(), r.col());
            }
        }
        System.out.printf("javatruth: %d definitions, %d references (%d compiler diagnostics)%n", sorted.size(), refs.size(), diagnostics);
    }

    private static String json(String value) {
        if (value == null) {
            return "null";
        }
        StringBuilder b = new StringBuilder("\"");
        for (char c : value.toCharArray()) {
            switch (c) {
                case '"' -> b.append("\\\"");
                case '\\' -> b.append("\\\\");
                case '\n' -> b.append("\\n");
                default -> b.append(c);
            }
        }
        return b.append('"').toString();
    }
}
