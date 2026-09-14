"""Adaptadores: cada ferramenta responde às mesmas perguntas sobre um símbolo.

references(symbol): linhas (arquivo, linha) que a ferramenta aponta como uso.
definition(symbol): trechos (arquivo, início, fim) candidatos à definição, na
ordem em que a ferramenta os devolve.

Cada resposta traz os bytes que um agente leria, isto é, a saída no formato
padrão da ferramenta, e a latência dessa chamada. As linhas saem de um formato
estruturado (JSON) quando a ferramenta tem um, para não depender de parsear
texto feito para humanos.
"""
import json
import os
import re
import shutil
import subprocess
import time
from dataclasses import dataclass, field
from pathlib import Path

from common import BENCH, DATA, repo_path

BIN = BENCH / "_tools" / "bin"
# ALL pede a lista inteira: por padrão o mira corta referências (50 no
# resolve, 200 no refs) com uma dica, bom para agente e injusto para recall.
ALL = 1_000_000
NPM_BIN = BENCH / "_tools" / "npm" / "bin"


@dataclass
class Answer:
    lines: list = field(default_factory=list)
    agent_bytes: int = 0
    ms: float = 0.0
    error: str = ""


def timed(cmd, cwd=None, timeout=300):
    start = time.perf_counter()
    try:
        proc = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return None, timeout * 1000.0
    return proc, (time.perf_counter() - start) * 1000.0


def word(name):
    return re.compile(rf"(?<![\w$]){re.escape(name)}(?![\w$])")


def ripgrep():
    """O ripgrep que os agentes do Claude Code usam vem embutido no binário
    `claude`, chamado com argv[0] = rg; um rg de verdade no PATH também serve."""
    real = shutil.which("rg")
    if real:
        return [real], None
    claude = os.environ.get("CLAUDE_CODE_EXECPATH") or str(Path.home() / ".local" / "bin" / "claude")
    return ["rg"], claude


class Grep:
    """Linha de base: ripgrep por palavra inteira, como um agente faria."""

    name = "grep"

    def __init__(self, repo):
        self.root = repo_path(repo)
        self.argv, self.executable = ripgrep()

    def _search(self, name):
        start = time.perf_counter()
        # Caminho explícito e stdin vazio: sem caminho, o ripgrep lê a entrada padrão quando ela não é um terminal.
        proc = subprocess.run([*self.argv, "-n", "-w", "-F", "--no-heading", "--color", "never", "-g", "!**/node_modules/**", "--", name, "."],
                              cwd=self.root, capture_output=True, text=True, executable=self.executable, stdin=subprocess.DEVNULL, timeout=120)
        ms = (time.perf_counter() - start) * 1000.0
        lines = []
        for row in proc.stdout.splitlines():
            parts = row.split(":", 2)
            if len(parts) >= 2 and parts[1].isdigit():
                lines.append((parts[0].removeprefix("./"), int(parts[1])))
        return Answer(lines=lines, agent_bytes=len(proc.stdout.encode()), ms=ms)

    def references(self, symbol):
        return self._search(symbol["name"])

    def definition(self, symbol):
        found = self._search(symbol["name"])
        found.lines = [(file, line, line) for file, line in found.lines]
        return found


class Mira:
    """mira: texto padrão para bytes, JSON para as posições."""

    name = "mira"

    def __init__(self, repo):
        self.root = repo_path(repo)
        self.bin = str(BIN / "mira")

    def _cmd(self, *args):
        return [self.bin, *args, "--repo", str(self.root), "--no-auto-index"]

    def _definitions(self, query):
        proc, _ = timed(self._cmd("resolve", query, "--include-refs", "--max-refs", str(ALL), "--json"))
        if proc is None or proc.returncode != 0 or not proc.stdout.strip():
            return []
        data = json.loads(proc.stdout)
        if isinstance(data, list):
            data = data[0] if data else {}
        return data.get("definitions") or []

    @staticmethod
    def _is_target(symbol, d):
        return (d.get("file") == symbol["file"] and d.get("name") == symbol["name"]
                and d.get("start_line", 0) <= symbol["line"] <= d.get("end_line", 0))

    def references(self, symbol):
        text, ms = timed(self._cmd("resolve", symbol["query"], "--include-refs", "--max-refs", str(ALL)))
        if text is None:
            return Answer(ms=ms, error="timeout")
        target = next((d for d in self._definitions(symbol["query"]) if self._is_target(symbol, d)), None)
        lines = []
        refs = (target or {}).get("refs") or {}
        for f in refs.get("files", []):
            lines += [(f["file"], r["line"]) for r in f["refs"]]
        error = "" if target else "definition not found"
        if refs.get("truncated"):
            error = "reference list truncated"
        return Answer(lines=lines, agent_bytes=len(text.stdout.encode()), ms=ms, error=error)

    def definition(self, symbol):
        text, ms = timed(self._cmd("resolve", symbol["query"]))
        if text is None:
            return Answer(ms=ms, error="timeout")
        lines = [(d["file"], d["start_line"], d["end_line"]) for d in self._definitions(symbol["query"])]
        return Answer(lines=lines, agent_bytes=len(text.stdout.encode()), ms=ms)


class MiraRefs(Mira):
    """`mira refs <nome>`: o que o agente recebe sem qualificar o nome,
    com as referências de todos os homônimos juntas."""

    name = "mira-refs"

    def _query(self, symbol):
        return symbol["name"]

    def references(self, symbol):
        text, ms = timed(self._cmd("refs", self._query(symbol), "--limit", str(ALL)))
        if text is None:
            return Answer(ms=ms, error="timeout")
        proc, _ = timed(self._cmd("refs", self._query(symbol), "--limit", str(ALL), "--json"))
        lines = []
        if proc is not None and proc.returncode == 0 and proc.stdout.strip():
            data = json.loads(proc.stdout)
            data = data[0] if isinstance(data, list) and data else data
            for f in data.get("files", []):
                lines += [(f["file"], r["line"]) for r in f["refs"]]
        return Answer(lines=lines, agent_bytes=len(text.stdout.encode()), ms=ms)

    def definition(self, symbol):
        return None


class MiraQualifiedRefs(MiraRefs):
    """`mira refs <Container.nome>`: com o nome qualificado, só as
    referências resolvidas para aquela definição; nome de topo sem ponto
    fica igual ao mira-refs."""

    name = "mira-qrefs"

    def _query(self, symbol):
        return symbol["query"]


class Probe:
    """Probe: busca exata no formato padrão (bytes) e em JSON (posições)."""

    name = "probe"

    def __init__(self, repo):
        self.root = repo_path(repo).resolve()
        self.bin = str(NPM_BIN / "probe")
        self._searches = {}  # nome -> (saída padrão, ms, resultados JSON): definição e referências usam a mesma busca

    def _rel(self, path):
        try:
            return Path(path).resolve().relative_to(self.root).as_posix()
        except ValueError:
            return str(path)

    def _results(self, name):
        proc, _ = timed([self.bin, "search", name, str(self.root), "--exact", "--allow-tests", "-o", "json", "--max-results", "10000"])
        if proc is None or proc.returncode != 0:
            return []
        try:
            return json.loads(proc.stdout).get("results", [])
        except json.JSONDecodeError:
            return []

    def _default(self, name):
        return timed([self.bin, "search", name, str(self.root), "--exact", "--allow-tests"])

    def _search(self, name):
        if name not in self._searches:
            text, ms = self._default(name)
            self._searches[name] = (text, ms, self._results(name) if text is not None else [])
        return self._searches[name]

    def references(self, symbol):
        text, ms, results = self._search(symbol["name"])
        if text is None:
            return Answer(ms=ms, error="timeout")
        lines = []
        for result in results:
            file = self._rel(result["file"])
            lines += [(file, m["start_line"]) for m in result.get("matches", [])
                      if m.get("kind") == "code" and m.get("text") == symbol["name"]]
        return Answer(lines=lines, agent_bytes=len(text.stdout.encode()), ms=ms)

    def definition(self, symbol):
        text, ms, results = self._search(symbol["name"])
        if text is None:
            return Answer(ms=ms, error="timeout")
        lines = [(self._rel(r["file"]), r["lines"][0], r["lines"][1]) for r in results]
        return Answer(lines=lines, agent_bytes=len(text.stdout.encode()), ms=ms)


class AiderMap:
    """Repo map do aider com orçamento fixo: não responde referências; na
    definição, acerta se a seção do arquivo no mapa cita o nome."""

    name = "aider-map"

    def __init__(self, repo, tokens=4096):
        path = DATA / "repomap" / f"{repo}-{tokens}.txt"
        text = path.read_text(errors="replace") if path.exists() else ""
        self.sections, self.bytes = parse_repo_map(text)

    def references(self, symbol):
        return None

    def definition(self, symbol):
        section = self.sections.get(symbol["file"], "")
        hit = bool(word(symbol["name"]).search(section))
        lines = [(symbol["file"], symbol["line"], symbol["line"])] if hit else []
        return Answer(lines=lines, agent_bytes=self.bytes, ms=0.0)


def parse_repo_map(text):
    """Separa o repo map por arquivo; o cabeçalho do aider antes do primeiro
    arquivo não conta nos bytes."""
    sections, current, size, started = {}, None, 0, False
    for line in text.split("\n"):
        header = line.endswith(":") and line and not line[0].isspace() and line[0] not in "│⋮"
        if header:
            started, current = True, line[:-1]
            sections[current] = ""
        elif started and current is not None:
            sections[current] += line + "\n"
        if started:
            size += len(line.encode()) + 1
    return sections, size
