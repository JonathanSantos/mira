#!/usr/bin/env python3
"""Junta os result.json das rodadas de agente numa tabela por tarefa e modelo.

Uso: python3 agent/report.py  (grava results/agent-<data>.md)
"""
import json
import sys
from collections import defaultdict
from datetime import date
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "static"))
from common import BENCH, DATA  # noqa: E402


def quality(r):
    if "precision" in r:
        text = f"P {r['precision']:.0%} R {r['recall']:.0%} (homônimos citados: {r['homonym_fp']})"
        if r.get("recall_within_1", r["recall"]) != r["recall"]:
            text += f"; com ±1 linha: P {r['precision_within_1']:.0%} R {r['recall_within_1']:.0%}"
        return text
    if "changed" in r:
        compiles = "" if "compiles" not in r else (", compila" if r["compiles"] else ", não compila")
        return f"mudou {r['changed']}, preservou {r['kept']}, {r['collateral_lines']} linhas a mais{compiles}"
    return r.get("answer", "sem resposta")


def main():
    results = [json.loads(p.read_text()) for p in sorted((DATA / "agent" / "runs").glob("*/result.json"))]
    groups = defaultdict(list)
    for r in results:
        groups[(r["task"], r["model"])].append(r)
    lines = [f"# Rodadas de agente ({date.today().isoformat()})", ""]
    for (task, model), rows in sorted(groups.items()):
        lines += [f"## {task} ({model})", "", "| braço | rep | resultado | chamadas | bytes lidos | tokens | tool uses | duração |",
                  "|---|---|---|---|---|---|---|---|"]
        for r in sorted(rows, key=lambda r: (r["arm"], r["rep"])):
            duration = f"{r['duration_ms'] / 1000:.0f} s" if r.get("duration_ms") else "–"
            lines.append(f"| {r['arm']} | {r['rep']} | {quality(r)} | {r['calls']} | {r['bytes_read']} | {r.get('tokens') or '–'} | "
                         f"{r.get('tool_uses') or '–'} | {duration} |")
        lines.append("")
    out = BENCH / "results" / f"agent-{date.today().isoformat()}.md"
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text("\n".join(lines))
    print("\n".join(lines))
    print(f"written to {out}")


if __name__ == "__main__":
    main()
