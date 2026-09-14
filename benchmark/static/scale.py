#!/usr/bin/env python3
"""Escala num repositório grande sem gabarito (o react): custo de indexar e de
responder, não qualidade.

Sorteia N funções e métodos do índice do mira, com semente fixa, e mede
para cada ferramenta os bytes e a latência de achar a definição e os usos.

Uso: python3 static/scale.py <repo> [--n 10] [--tools grep,mira,mira-refs,probe,serena]
"""
import argparse
import contextlib
import random
import sqlite3
import time

import adapters
from common import DATA, repo_path, write_json


def pick_symbols(repo, n):
    db = sqlite3.connect(repo_path(repo) / ".mira" / "index.db")
    rows = db.execute(
        "SELECT s.name, f.path, s.start_line, s.qualified_name FROM symbols s JOIN files f ON f.id = s.file_id "
        "WHERE s.kind IN ('function', 'method') AND length(s.name) > 3 ORDER BY f.path, s.start_line").fetchall()
    rng = random.Random(f"20260914:{repo}:scale")
    return [{"name": name, "file": path, "line": line, "query": qualified or name, "id": f"{path}:{line}", "container": None}
            for name, path, line, qualified in rng.sample(rows, min(n, len(rows)))]


def quantile(values, q):
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(round(q * (len(ordered) - 1))))] if ordered else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repo")
    parser.add_argument("--n", type=int, default=10)
    parser.add_argument("--tools", default="grep,mira,mira-refs,probe,serena")
    args = parser.parse_args()
    symbols = pick_symbols(args.repo, args.n)
    summary = {"repo": args.repo, "symbols": [s["query"] for s in symbols], "tools": {}}
    for name in args.tools.split(","):
        started = time.perf_counter()
        if name == "serena":
            from serena_adapter import Serena
            manager = Serena(args.repo)
        else:
            factory = {"grep": adapters.Grep, "mira": adapters.Mira, "mira-refs": adapters.MiraRefs, "probe": adapters.Probe}[name]
            manager = contextlib.nullcontext(factory(args.repo))
        rows = []
        try:
            with manager as tool:
                for symbol in symbols:
                    for question in ("definition", "references"):
                        answer = getattr(tool, question)(symbol)
                        if answer is not None:
                            rows.append((question, answer))
                startup = getattr(tool, "startup_ms", 0.0)
        except Exception as exc:  # uma ferramenta que não sobe no repositório grande é um resultado, não um crash do script
            summary["tools"][name] = {"error": str(exc)[:300]}
            print(f"{name}: failed: {exc}")
            continue
        stats = {}
        for question in ("definition", "references"):
            answers = [a for q, a in rows if q == question]
            if answers:
                stats[question] = {"bytes_p50": quantile([a.agent_bytes for a in answers], 0.5), "ms_p50": round(quantile([a.ms for a in answers], 0.5)),
                                   "ms_p90": round(quantile([a.ms for a in answers], 0.9)), "errors": sum(1 for a in answers if a.error)}
        summary["tools"][name] = {"startup_ms": round(startup), "total_s": round(time.perf_counter() - started, 1), **stats}
        print(f"{name}: {summary['tools'][name]}", flush=True)
    write_json(DATA / "scale" / f"{args.repo}.json", summary)


if __name__ == "__main__":
    main()
