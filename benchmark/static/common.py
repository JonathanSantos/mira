"""Utilidades compartilhadas pela suíte estática do benchmark."""
import json
import re
from collections import defaultdict
from pathlib import Path

BENCH = Path(__file__).resolve().parent.parent
DATA = BENCH / "_data"

# Arquivos fora do código medido: documentação e exemplos distorcem as
# contagens e nenhuma tarefa real navega por eles.
EXCLUDED_PATHS = re.compile(r"(^|/)(docs|examples|scripts|fixtures|benchmarks?)/")


def repo_path(name):
    return BENCH / "_repos" / name


def manifest():
    data = json.loads((BENCH / "repos.json").read_text())
    return {repo["name"]: repo for repo in data["repos"]}


def read_jsonl(path):
    with open(path) as f:
        return [json.loads(line) for line in f if line.strip()]


def write_json(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")


def sample_path(repo, sample_set=""):
    """Amostra de símbolos: <repo>.json na rodada publicada, <repo>.<set>.json nas outras."""
    return DATA / "samples" / (f"{repo}.{sample_set}.json" if sample_set else f"{repo}.json")


def answers_path(repo, sample_set=""):
    """Respostas das ferramentas para a amostra, uma por linha."""
    return DATA / "static" / (f"{repo}.{sample_set}.jsonl" if sample_set else f"{repo}.jsonl")


class Truth:
    """Gabarito de um repositório: definições e referências por linha."""

    def __init__(self, repo):
        self.root = repo_path(repo)
        # Gabarito de compilador (Go, TypeScript, Java) é completo: nome na linha
        # sem referência resolvida é comentário, string ou outro símbolo. No
        # Python o jedi deixa usos dinâmicos sem resolver, e só lá existe "unknown".
        self.complete = manifest()[repo]["lang"] in ("go", "typescript", "java")
        base = DATA / "groundtruth" / repo
        self.defs = {d["id"]: d for d in read_jsonl(base / "defs.jsonl")}
        self.refs = defaultdict(set)  # def id -> {(arquivo, linha)}
        self.at = defaultdict(set)  # (arquivo, linha) -> {def ids referenciados ali}
        for ref in read_jsonl(base / "refs.jsonl"):
            if ref["def"] not in self.defs:
                continue
            key = (ref["file"], ref["line"])
            self.refs[ref["def"]].add(key)
            self.at[key].add(ref["def"])
        self.by_name = defaultdict(list)
        self.declared = defaultdict(set)  # nome -> {(arquivo, linha)} das declarações com esse nome
        for d in self.defs.values():
            self.by_name[d["name"]].append(d["id"])
            self.declared[d["name"]].add((d["file"], d["line"]))
        # Arquivos que o gabarito analisou. Fora deles (build tag, configuração)
        # o gabarito não diz se um uso é do símbolo.
        self.files = {d["file"] for d in self.defs.values()} | {file for file, _ in self.at}
        self._files = {}

    def text(self, key):
        """Texto de uma linha do repositório, com cache por arquivo."""
        file, line = key
        if file not in self._files:
            try:
                self._files[file] = (self.root / file).read_text(errors="replace").split("\n")
            except OSError:
                self._files[file] = []
        lines = self._files[file]
        return lines[line - 1] if 0 < line <= len(lines) else ""

    def judge(self, def_id, lines):
        """Classifica as linhas que uma ferramenta devolveu para o símbolo.

        tp: o gabarito tem referência ao símbolo na linha.
        fp: a linha não tem o nome como palavra, só declara um homônimo, o
            gabarito resolve o nome ali para outro símbolo, ou (gabarito de
            compilador, arquivo analisado) não há referência nenhuma ali.
        unknown: o nome está na linha e o gabarito não decide: no Python o
            jedi não resolveu aquele uso; em qualquer linguagem, o arquivo
            ficou fora do gabarito. Fica fora da precision e é reportado à parte.
        fn: referências do gabarito que a ferramenta não devolveu.
        """
        target = self.defs[def_id]
        expected = self.refs[def_id]
        declaration = (target["file"], target["line"])
        pattern = re.compile(rf"(?<![\w$]){re.escape(target['name'])}(?![\w$])")
        verdict = {"tp": set(), "fp": set(), "unknown": set()}
        for key in set(lines) - {declaration}:
            uses = len(pattern.findall(self.text(key)))
            if key in expected:
                verdict["tp"].add(key)
            elif uses == 0:
                verdict["fp"].add(key)
            elif key in self.declared[target["name"]] and (self.complete or uses == 1):
                # A linha declara um homônimo. No Python, se o nome aparece de novo
                # (class Flask(flask.Flask)), o jedi pode só não ter visto o uso.
                verdict["fp"].add(key)
            elif any(self.defs[other]["name"] == target["name"] for other in self.at.get(key, ())):
                verdict["fp"].add(key)
            elif self.complete and key[0] in self.files:
                verdict["fp"].add(key)
            else:
                verdict["unknown"].add(key)
        verdict["fn"] = expected - verdict["tp"]
        return verdict
