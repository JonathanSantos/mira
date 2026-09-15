#!/usr/bin/env python3
"""Roda a suíte estática num repositório: cada ferramenta responde às mesmas
perguntas sobre os símbolos sorteados, e cada resposta vira uma linha em
_data/static/<repo>.jsonl (a pontuação fica com score.py).

Uso: python3 static/run.py <repo> [--tools grep,mira,...] [--limit N] [--set NOME]
"""
import argparse
import contextlib
import json
import time

import adapters
from common import answers_path, sample_path

TOOLS = ["grep", "mira", "mira-refs", "mira-qrefs", "probe", "serena", "aider-map"]
QUESTIONS = ("definition", "references")


def make_tool(name, repo):
    if name == "serena":
        from serena_adapter import Serena  # só importa quem sobe language server
        return Serena(repo)
    factories = {"grep": adapters.Grep, "mira": adapters.Mira, "mira-refs": adapters.MiraRefs,
                 "mira-qrefs": adapters.MiraQualifiedRefs,
                 "probe": adapters.Probe, "aider-map": adapters.AiderMap}
    return contextlib.nullcontext(factories[name](repo))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repo")
    parser.add_argument("--tools", default=",".join(TOOLS))
    parser.add_argument("--limit", type=int, default=0, help="only the first N symbols (smoke runs)")
    parser.add_argument("--set", default="", help="named sample from sample.py --set")
    args = parser.parse_args()
    symbols = json.loads(sample_path(args.repo, args.set).read_text())["symbols"]
    if args.limit:
        symbols = symbols[: args.limit]
    out = answers_path(args.repo, args.set)
    out.parent.mkdir(parents=True, exist_ok=True)
    tools = args.tools.split(",")
    kept = [row for row in _existing(out) if row["tool"] not in tools]
    with open(out, "w") as f:
        for row in kept:
            f.write(json.dumps(row) + "\n")
        for name in tools:
            started = time.perf_counter()
            with make_tool(name, args.repo) as tool:
                tool.definition(symbols[0])  # aquecimento: cache de disco, language server, JIT
                for symbol in symbols:
                    for question in QUESTIONS:
                        answer = getattr(tool, question)(symbol)
                        if answer is None:
                            continue
                        f.write(json.dumps({"tool": name, "question": question, "symbol": symbol["id"],
                                            "lines": answer.lines, "agent_bytes": answer.agent_bytes,
                                            "ms": round(answer.ms, 1), "error": answer.error}) + "\n")
                        f.flush()
            print(f"{args.repo} {name}: {len(symbols)} symbols in {time.perf_counter() - started:.1f}s", flush=True)


def _existing(path):
    """Mantém as linhas de ferramentas que não estão sendo re-medidas."""
    if not path.exists():
        return []
    with open(path) as f:
        return [json.loads(line) for line in f if line.strip()]


if __name__ == "__main__":
    main()
