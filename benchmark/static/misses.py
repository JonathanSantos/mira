#!/usr/bin/env python3
"""Mostra o que uma ferramenta errou na suíte estática, agrupado por padrão.

As referências perdidas (fn) e as sobrando (fp) são classificadas pelo texto
da linha em volta do nome: chamada qualificada, chamada solta, chave de
literal, acesso a campo, uso como tipo, import. É a análise de erro que diz o
que corrigir, não só quanto errou.

Uso: python3 static/misses.py <repo> <ferramenta> [--show N] [--set NOME]
"""
import argparse
import re
from collections import Counter, defaultdict

from common import Truth, answers_path, read_jsonl


def category(text, name):
    n = re.escape(name)
    patterns = [
        ("import", rf"^\s*(import|from)\b.*\b{n}\b"),
        ("literal key", rf"(?<![\w.]){n}\s*:(?!:)"),
        ("method call x.name(", rf"\.\s*{n}\s*\("),
        ("field access x.name", rf"\.\s*{n}\b(?!\s*\()"),
        ("call name(", rf"(?<![\w.]){n}\s*\("),
        ("type position", rf"[\[\]*(,:]\s*{n}\b|\b{n}\s*[{{<]"),
    ]
    for label, pattern in patterns:
        if re.search(pattern, text):
            return label
    return "other"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repo")
    parser.add_argument("tool")
    parser.add_argument("--show", type=int, default=3)
    parser.add_argument("--set", default="", help="named sample from sample.py --set")
    args = parser.parse_args()
    truth = Truth(args.repo)
    rows = [r for r in read_jsonl(answers_path(args.repo, args.set)) if r["tool"] == args.tool and r["question"] == "references"]
    buckets = {"fn": defaultdict(list), "fp": defaultdict(list)}
    for row in rows:
        target = truth.defs[row["symbol"]]
        verdict = truth.judge(row["symbol"], [tuple(x) for x in row["lines"]])
        for kind in ("fn", "fp"):
            for key in sorted(verdict[kind]):
                text = truth.text(key).strip()
                label = f"{target['kind']}: {category(text, target['name'])}"
                buckets[kind][label].append(f"{key[0]}:{key[1]} [{target.get('container') or ''}.{target['name']}] {text[:110]}")
    for kind, title in (("fn", "perdidas (fn)"), ("fp", "sobrando (fp)")):
        total = sum(len(v) for v in buckets[kind].values())
        print(f"\n== {args.tool} em {args.repo}: {total} referências {title}")
        for label, items in sorted(buckets[kind].items(), key=lambda kv: -len(kv[1])):
            print(f"  {len(items):4d}  {label}")
            for example in items[: args.show]:
                print(f"          {example}")


if __name__ == "__main__":
    main()
