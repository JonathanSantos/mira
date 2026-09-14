#!/usr/bin/env python3
"""Pontua a suíte estática contra o gabarito e escreve as tabelas em markdown.

Referências: precision e recall por linha (micro, somando todos os símbolos),
recall médio por símbolo, a fração "unknown" (o nome está na linha e o
gabarito não resolveu o uso) e as mesmas medidas só nos símbolos com
homônimos. Definição: acerto no primeiro candidato e em qualquer candidato.
Custo: bytes que o agente leria e latência, em mediana e p90.

Uso: python3 static/score.py <repo> [<repo> ...]
"""
import sys
from collections import defaultdict
from datetime import date

from common import BENCH, DATA, Truth, read_jsonl


def quantile(values, q):
    if not values:
        return 0
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(round(q * (len(ordered) - 1))))]


def ratio(num, den):
    return num / den if den else None


def pct(value):
    return "–" if value is None else f"{value:.0%}"


def refs_metrics(truth, rows, symbols):
    tp = fp = unknown = fn = 0
    per_symbol = []
    for row in rows:
        verdict = truth.judge(row["symbol"], [tuple(x) for x in row["lines"]])
        tp, fp, unknown, fn = tp + len(verdict["tp"]), fp + len(verdict["fp"]), unknown + len(verdict["unknown"]), fn + len(verdict["fn"])
        per_symbol.append(ratio(len(verdict["tp"]), len(verdict["tp"]) + len(verdict["fn"])) or 0.0)
    return {"precision": ratio(tp, tp + fp), "recall": ratio(tp, tp + fn), "recall_avg": ratio(sum(per_symbol), len(per_symbol)),
            "unknown": ratio(unknown, tp + fp + unknown), "n": len(rows)}


def def_hit(truth, row):
    target = truth.defs[row["symbol"]]
    hits = [i for i, (file, start, end) in enumerate(row["lines"]) if file == target["file"] and start <= target["line"] <= end]
    return (hits[0] == 0) if hits else False, bool(hits)


def cost(rows):
    return {"bytes_p50": quantile([r["agent_bytes"] for r in rows], 0.5), "bytes_p90": quantile([r["agent_bytes"] for r in rows], 0.9),
            "ms_p50": quantile([r["ms"] for r in rows], 0.5), "ms_p90": quantile([r["ms"] for r in rows], 0.9),
            "errors": sum(1 for r in rows if r["error"])}


def score(repo):
    truth = Truth(repo)
    rows = read_jsonl(DATA / "static" / f"{repo}.jsonl")
    sample = {s["id"]: s for s in __import__("json").loads((DATA / "samples" / f"{repo}.json").read_text())["symbols"]}
    by = defaultdict(list)
    for row in rows:
        by[(row["tool"], row["question"])].append(row)
    tools = sorted({row["tool"] for row in rows})
    lines = [f"### {repo}", "", f"Referências ({len(sample)} símbolos sorteados, {sum(s['homonym'] for s in sample.values())} com homônimos)", "",
             "| ferramenta | precision | recall | recall médio | unknown | precision homônimos | recall homônimos | bytes p50 | bytes p90 | ms p50 | erros |",
             "|---|---|---|---|---|---|---|---|---|---|---|"]
    for tool in tools:
        refs = by.get((tool, "references"), [])
        if not refs:
            continue
        m = refs_metrics(truth, refs, sample)
        h = refs_metrics(truth, [r for r in refs if sample[r["symbol"]]["homonym"]], sample)
        c = cost(refs)
        lines.append(f"| {tool} | {pct(m['precision'])} | {pct(m['recall'])} | {pct(m['recall_avg'])} | {pct(m['unknown'])} | "
                     f"{pct(h['precision'])} | {pct(h['recall'])} | {c['bytes_p50']} | {c['bytes_p90']} | {c['ms_p50']:.0f} | {c['errors']} |")
    lines += ["", "Definição", "", "| ferramenta | acerto no 1º | acerto em algum | candidatos p50 | bytes p50 | bytes p90 | ms p50 | erros |",
              "|---|---|---|---|---|---|---|---|"]
    for tool in tools:
        defs = by.get((tool, "definition"), [])
        if not defs:
            continue
        first = sum(def_hit(truth, r)[0] for r in defs)
        anywhere = sum(def_hit(truth, r)[1] for r in defs)
        c = cost(defs)
        lines.append(f"| {tool} | {pct(ratio(first, len(defs)))} | {pct(ratio(anywhere, len(defs)))} | "
                     f"{quantile([len(r['lines']) for r in defs], 0.5)} | {c['bytes_p50']} | {c['bytes_p90']} | {c['ms_p50']:.0f} | {c['errors']} |")
    return "\n".join(lines) + "\n"


def main():
    out = BENCH / "results" / f"static-{date.today().isoformat()}.md"
    out.parent.mkdir(parents=True, exist_ok=True)
    body = "\n".join(score(repo) for repo in sys.argv[1:])
    out.write_text(f"# Suíte estática ({date.today().isoformat()})\n\n{body}")
    print(body)
    print(f"written to {out}")


if __name__ == "__main__":
    main()
