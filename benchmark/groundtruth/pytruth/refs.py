"""Gabarito Python: definições e referências pelo jedi.

Uso: python refs.py <raiz do repositório> <diretório de saída>
Grava defs.jsonl e refs.jsonl no mesmo formato do gotruth.
"""
import json
import os
import sys

import jedi

SKIP = {".git", "node_modules", ".venv", "venv", "build", "dist", ".mira", ".serena", "__pycache__", ".tox"}


def py_files(root):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP and not d.startswith("."))
        for name in sorted(filenames):
            if name.endswith(".py"):
                yield os.path.join(dirpath, name)


def kind_of(name):
    """Tipo das declarações que as ferramentas tratam como símbolo."""
    parent = name.parent()
    if name.type == "class":
        return "class"
    if name.type == "function":
        return "method" if parent and parent.type == "class" else "function"
    if name.type == "statement" and parent:
        if parent.type == "module":
            return "variable"
        if parent.type == "class":
            return "attribute"
        if parent.type == "function" and f"self.{name.name}" in (name.get_line_code() or ""):
            return "attribute"
    return ""


def container_of(name):
    parent = name.parent()
    if parent and parent.type == "class":
        return parent.name
    if parent and parent.type == "function":
        grand = parent.parent()
        if grand and grand.type == "class":
            return grand.name
    return None


def main():
    root = os.path.abspath(sys.argv[1])
    out = os.path.abspath(sys.argv[2])
    project = jedi.Project(root)
    defs, refs = {}, set()

    def rel(path):
        return os.path.relpath(str(path), root).replace(os.sep, "/")

    def in_repo(path):
        return path is not None and str(path).startswith(root + os.sep)

    def register(name):
        kind = kind_of(name)
        if not kind or not in_repo(name.module_path):
            return None
        ident = f"{rel(name.module_path)}:{name.line}:{name.column + 1}"
        if ident not in defs:
            defs[ident] = {"id": ident, "name": name.name, "kind": kind, "container": container_of(name),
                           "file": rel(name.module_path), "line": name.line, "col": name.column + 1}
        return ident

    files = list(py_files(root))
    for index, path in enumerate(files, 1):
        script = jedi.Script(path=path, project=project)
        for name in script.get_names(all_scopes=True, definitions=True, references=True):
            try:
                targets = name.goto(follow_imports=True, follow_builtin_imports=False)
            except Exception:  # jedi falha em construções raras; a ref só não entra no gabarito
                targets = []
            here = (rel(path), name.line, name.column)
            elsewhere = [t for t in targets if in_repo(t.module_path) and (rel(t.module_path), t.line, t.column) != here]
            if name.is_definition() and not elsewhere:
                register(name)
                continue
            # Um nome de import (`from .app import Flask`) o jedi marca como
            # definição; seguindo o import ele cai na declaração original, e aí
            # é referência, como no gabarito de TypeScript e de Java.
            for target in elsewhere if name.is_definition() else targets:
                ident = register(target)
                if ident and not (rel(path) == defs[ident]["file"] and name.line == defs[ident]["line"]):
                    refs.add((ident, rel(path), name.line, name.column + 1))
        if index % 20 == 0:
            print(f"pytruth: {index}/{len(files)} files", file=sys.stderr, flush=True)

    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "defs.jsonl"), "w") as f:
        for ident in sorted(defs):
            f.write(json.dumps(defs[ident]) + "\n")
    with open(os.path.join(out, "refs.jsonl"), "w") as f:
        for ident, file, line, col in sorted(refs):
            f.write(json.dumps({"def": ident, "file": file, "line": line, "col": col}) + "\n")
    print(f"pytruth: {len(defs)} definitions, {len(refs)} references")


if __name__ == "__main__":
    main()
