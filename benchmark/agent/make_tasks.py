#!/usr/bin/env python3
"""Gera tarefas de agente a partir do gabarito, com a resposta já conhecida.

impact: listar os usos em código de produção de um símbolo que tem homônimos.
    A chave são as referências do gabarito fora de testes.
rename: renomear um símbolo sem tocar nos homônimos. A chave são as linhas que
    precisam mudar (declaração e todas as referências, testes incluídos) e as
    que não podem mudar (homônimos e seus usos).

Símbolos com homônimo declarado numa interface ficam de fora do rename:
renomear só a implementação quebraria o contrato, e a tarefa ficaria ambígua.

Uso: python3 agent/make_tasks.py <repo> [<repo> ...]
"""
import random
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "static"))
from common import BENCH, EXCLUDED_PATHS, Truth, write_json  # noqa: E402

SEED = 20260914
TEST_PATH = re.compile(r"(^|/)(tests?|__tests__|e2e|testdata|test)/|_test\.go$|\.(test|spec)\.[jt]sx?$|(^|/)test_[^/]*\.py$|_test\.py$|conftest\.py$")


def is_test(path):
    return bool(TEST_PATH.search(path))


def label(d):
    return f"{d['container']}.{d['name']}" if d.get("container") else d["name"]


def sites(lines):
    return sorted(f"{file}:{line}" for file, line in lines)


def candidates(truth):
    interfaces = {d["name"] for d in truth.defs.values() if d["kind"] == "interface"}
    for d in truth.defs.values():
        if d["kind"] not in ("method", "function") or EXCLUDED_PATHS.search(d["file"]) or is_test(d["file"]):
            continue
        homonyms = [h for h in truth.by_name[d["name"]] if h != d["id"]]
        refs = truth.refs[d["id"]]
        yield {
            "def": d,
            "refs": refs,
            "prod": {r for r in refs if not is_test(r[0])},
            "homonyms": homonyms,
            "trap": set().union(*(truth.refs[h] for h in homonyms)) if homonyms else set(),
            "contract": any(truth.defs[h].get("container") in interfaces for h in homonyms) or d.get("container") in interfaces,
        }


def impact_task(repo, c):
    d = c["def"]
    return {
        "id": f"{repo}-impact", "kind": "impact", "symbol": d["id"], "label": label(d),
        "prompt": (f"List every place in production code (exclude tests) that references {label(d)}, the {d['kind']} "
                   f"declared at {d['file']}:{d['line']}. Other symbols are also named {d['name']}; do not include their uses. "
                   'Write the answer as JSON to the answer file: {"sites": ["path/to/file:line", ...]}, one entry per referencing line, '
                   "paths relative to the repository root."),
        "key": {"sites": sites(c["prod"]), "homonym_sites": sites(r for r in c["trap"] if not is_test(r[0]))},
    }


def rename_task(repo, truth, c):
    d = c["def"]
    new_name = f"{d['name']}V2"
    homonym_decls = {(truth.defs[h]["file"], truth.defs[h]["line"]) for h in c["homonyms"]}
    return {
        "id": f"{repo}-rename", "kind": "rename", "symbol": d["id"], "label": label(d),
        "prompt": (f"Rename {label(d)}, the {d['kind']} declared at {d['file']}:{d['line']}, to {new_name}. Update every reference, "
                   f"tests included, so the project still builds. Other symbols named {d['name']} must stay unchanged. "
                   "When you are done, reply with a one-line summary."),
        "key": {"old_name": d["name"], "new_name": new_name,
                "must_change": sites(set(c["refs"]) | {(d["file"], d["line"])}),
                "must_keep": sites(c["trap"] | homonym_decls)},
    }


def make(repo):
    truth = Truth(repo)
    rng = random.Random(f"{SEED}:{repo}:agent")
    pool = sorted(candidates(truth), key=lambda c: c["def"]["id"])
    impact_pool = [c for c in pool if c["homonyms"] and 4 <= len(c["prod"]) <= 15 and len({f for f, _ in c["prod"]}) >= 2]
    impact = rng.choice(impact_pool) if impact_pool else None
    rename_pool = [c for c in pool if not c["contract"] and len(c["trap"]) >= 2 and 3 <= len(c["refs"]) <= 25
                   and len({f for f, _ in c["refs"]}) >= 2 and (not impact or c["def"]["id"] != impact["def"]["id"])]
    rename = rng.choice(rename_pool) if rename_pool else None
    tasks = [t for t in (impact and impact_task(repo, impact), rename and rename_task(repo, truth, rename)) if t]
    write_json(BENCH / "agent" / "tasks" / f"{repo}.json", {"repo": repo, "seed": SEED, "tasks": tasks})
    for t in tasks:
        size = len(t["key"].get("sites") or t["key"].get("must_change"))
        trap = len(t["key"].get("homonym_sites") or t["key"].get("must_keep") or [])
        print(f"{t['id']}: {t['label']} ({t['symbol']}) key={size} trap={trap}")


if __name__ == "__main__":
    for name in sys.argv[1:]:
        make(name)
