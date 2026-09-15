#!/usr/bin/env python3
"""Recall do Mira com intervalo de confiança e a causa de cada referência perdida.

Lê as respostas de uma amostra (run.py --set) e, para cada referência do
gabarito que a ferramenta não devolveu, consulta o índice do próprio Mira
(_repos/<repo>/.mira/index.db) para dizer por quê: o uso nem foi extraído,
ficou ambíguo, foi marcado como externo, foi resolvido para outra definição
ou ficou sem resolver. Quando falta o tipo do receiver, o texto do arquivo
diz de onde ele veio (for-of, desestruturação, parâmetro de callback,
resultado de chamada...). Essa última parte é classificação por padrão de
texto: os exemplos de cada causa servem para conferir.

O intervalo de 95% do recall vem de reamostrar os símbolos (bootstrap com
semente fixa). A precisão é o portão: com qualquer referência errada o
script escreve o relatório e sai com código 1.

Uso: python3 static/recall.py [--set recall] [--tool mira] [--show 3] <repo> [<repo> ...]
"""
import argparse
import random
import re
import sqlite3
import sys
from collections import Counter, defaultdict
from datetime import date

from common import BENCH, Truth, answers_path, manifest, read_jsonl, repo_path, sample_path
from misses import category
from sample import SEED

BOOTSTRAP = 1000
LOOKBACK = 300  # linhas acima do uso onde se procura a declaração do receiver

# Onde o receiver foi declarado, por linguagem, na ordem em que os padrões são
# tentados em cada linha. NAME vira o nome do receiver.
ORIGINS = {
    "typescript": [
        ("for-of variable", r"\bfor\s*\(\s*(?:const|let|var)\s+[^;]*\bNAME\b[^;]*\bof\b"),
        ("destructuring", r"\b(?:const|let|var)\s+[{\[][^=]*\bNAME\b[^=]*[}\]]\s*(?::[^=]*)?="),
        ("callback parameter", r"(?:\([^()]*\bNAME\b[^()]*\)|\bNAME)\s*(?::\s*[^=]+?)?=>"),
        ("function parameter", r"\bfunction\b[^(]*\([^)]*\bNAME\b"),
        ("variable from a call", r"\b(?:const|let|var)\s+NAME\s*(?::[^=]+)?=\s*(?:await\s+)?[\w$.]+(?:<[^>]*>)?\s*\("),
        ("variable", r"\b(?:const|let|var)\s+NAME\b"),
        ("import", r"^\s*import\b.*\bNAME\b"),
    ],
    "python": [
        ("for variable", r"\bfor\s+[^:]*\bNAME\b[^:]*\bin\b"),
        ("with target", r"\bwith\b[^:]*\bas\s+\(?[^:]*\bNAME\b"),
        ("lambda parameter", r"\blambda\b[^:]*\bNAME\b"),
        ("function parameter", r"\bdef\s+\w+\s*\([^)]*\bNAME\b"),
        ("variable from a call", r"^\s*NAME\s*(?::[^=]+)?=\s*(?:await\s+)?[\w.]+\s*\("),
        ("tuple unpacking", r"^\s*[\w\s,()*]*\bNAME\b[\w\s,()*]*,[\w\s,()*]*=|^\s*[\w\s,()*]*,\s*\*?NAME\b[\w\s()*]*="),
        ("variable", r"^\s*NAME\s*(?::[^=]+)?="),
        ("import", r"^\s*(?:from\b.*\bimport\b|import\b).*\bNAME\b"),
    ],
    "java": [
        ("for-each variable", r"\bfor\s*\([^;:]*\bNAME\s*:"),
        ("lambda parameter", r"(?:\([^()]*\bNAME\b[^()]*\)|\bNAME)\s*->"),
        ("var (inferred)", r"\bvar\s+NAME\s*[=:]"),
        ("declared with a type", r"\b[A-Z][\w.]*(?:<[^;()]*>)?(?:\[\])*\s+NAME\s*[=;,)]"),
    ],
    "go": [
        ("range variable", r"\bfor\b[^{]*\bNAME\b[^{]*:=\s*range\b"),
        ("short variable from a call", r"\bNAME\b[\w\s,]*:=\s*[\w.]+(?:\[[^\]]*\])?\s*\("),
        ("short variable", r"\bNAME\b[\w\s,]*:="),
        ("function parameter", r"\bfunc\b[^{]*\([^)]*\bNAME\s+[\w.*\[\]]"),
        ("var declaration", r"\bvar\s+NAME\b"),
    ],
}
PARAMETERS = {"callback parameter", "function parameter", "lambda parameter"}


class Index:
    """Consulta as refs gravadas pelo Mira numa linha de um arquivo."""

    def __init__(self, repo):
        self.conn = sqlite3.connect(repo_path(repo) / ".mira" / "index.db")
        self.conn.row_factory = sqlite3.Row

    def refs_at(self, file, line, name):
        return self.conn.execute(
            """SELECT r.resolution, r.receiver, r.receiver_type, r.receiver_path, s.name AS target_name, tf.path AS target_file,
                      s.start_line AS target_start, s.end_line AS target_end
               FROM refs r JOIN files f ON f.id = r.file_id
               LEFT JOIN symbols s ON s.id = r.resolved_symbol_id
               LEFT JOIN files tf ON tf.id = s.file_id
               WHERE f.path = ? AND r.line = ? AND r.name = ?""", (file, line, name)).fetchall()


def origin(truth, lang, key, receiver, path):
    """De onde veio um receiver sem tipo, olhando o uso e as linhas acima.

    receiver é a raiz da cadeia depois de this/self e path, os membros do
    meio: em this.app.scene.get(), receiver "app" e path "scene".
    """
    root = receiver.split(".")[0].strip()
    if root in ("this", "self", "cls", "super"):
        return "this/self member"
    via_self = re.search(rf"\b(?:this|self)\s*\??\.\s*{re.escape(root)}\b", truth.text(key))
    if path:
        return "member chain from this/self" if via_self else "member chain"
    if via_self:
        return "this/self field"
    if "(" in receiver or "[" in receiver:
        return "call or index result"
    file, line = key
    patterns = [(label, re.compile(p.replace("NAME", re.escape(root)))) for label, p in ORIGINS.get(lang, [])]
    for n in range(line, max(0, line - LOOKBACK), -1):
        text = truth.text((file, n))
        for label, pattern in patterns:
            if not pattern.search(text):
                continue
            if label in PARAMETERS:
                typed = re.search(rf"\b{re.escape(root)}\s*\??\s*:", text) if lang in ("typescript", "python") else True
                return f"{label}, {'typed' if typed else 'untyped'}"
            return label
    return "declared elsewhere"


def cause(truth, index, lang, target, key):
    text = truth.text(key)
    refs = index.refs_at(key[0], key[1], target["name"])
    if not refs:
        return f"not extracted ({category(text.strip(), target['name'])})"
    resolved = [r for r in refs if r["resolution"] == "resolved"]
    if any(r["target_file"] == target["file"] and r["target_start"] <= target["line"] <= r["target_end"] for r in resolved):
        return "resolved but missing from the answer"
    if any(r["target_file"] == target["file"] and r["target_name"] == target["name"] for r in resolved):
        return "resolved to another overload"  # outra declaração do mesmo nome no arquivo
    if resolved:
        return "resolved to another definition"
    statuses = {r["resolution"] for r in refs}
    if "ambiguous" in statuses:
        return "ambiguous"
    if "external" in statuses:
        return "marked external"
    ref = max(refs, key=lambda r: (bool(r["receiver"]), bool(r["receiver_path"])))
    if not ref["receiver"] and ref["receiver_path"]:
        return "unresolved, chain from a function call"  # numberHeap().size()
    if not ref["receiver"]:
        return "unresolved, bare name"
    if ref["receiver_type"]:
        return "unresolved, member not found on the receiver type"
    return f"unresolved, receiver without type ({origin(truth, lang, key, ref['receiver'], ref['receiver_path'])})"


def interval(per_symbol, rng):
    """Intervalo de 95% do recall (micro), reamostrando símbolos."""
    values = []
    for _ in range(BOOTSTRAP):
        tp = fn = 0
        for _ in per_symbol:
            s_tp, s_fn = per_symbol[rng.randrange(len(per_symbol))]
            tp, fn = tp + s_tp, fn + s_fn
        values.append(tp / (tp + fn) if tp + fn else 0.0)
    values.sort()
    return values[int(0.025 * BOOTSTRAP)], values[int(0.975 * BOOTSTRAP) - 1]


def example(key, name, text):
    return f"`{key[0]}:{key[1]}` {name}: `" + text.strip()[:90].replace("`", "'").replace("|", "\\|") + "`"


def measure(repo, sample_set, tool, rng):
    truth = Truth(repo)
    lang = manifest()[repo]["lang"]
    index = Index(repo)
    rows = [r for r in read_jsonl(answers_path(repo, sample_set)) if r["tool"] == tool and r["question"] == "references"]
    tp = fp = unknown = 0
    per_symbol, wrong = [], []
    causes = defaultdict(list)
    for row in rows:
        target = truth.defs[row["symbol"]]
        verdict = truth.judge(row["symbol"], [tuple(x) for x in row["lines"]])
        tp, fp = tp + len(verdict["tp"]), fp + len(verdict["fp"])
        unknown += len(verdict["unknown"])
        per_symbol.append((len(verdict["tp"]), len(verdict["fn"])))
        wrong += [example(key, target["name"], truth.text(key)) for key in sorted(verdict["fp"])]
        for key in sorted(verdict["fn"]):
            causes[cause(truth, index, lang, target, key)].append(example(key, target["name"], truth.text(key)))
    fn = sum(len(v) for v in causes.values())
    low, high = interval(per_symbol, rng) if per_symbol else (0.0, 0.0)
    return {"repo": repo, "lang": lang, "symbols": len(rows), "tp": tp, "fp": fp, "fn": fn, "unknown": unknown,
            "low": low, "high": high, "causes": causes, "wrong": wrong}


def pct(num, den):
    return f"{num / den:.0%}" if den else "–"


def section(m, show):
    lines = [f"## {m['repo']} ({m['lang']}, {m['symbols']} símbolos)", "",
             "| precision | recall | IC 95% do recall | certas | erradas | perdidas | sem gabarito |", "|---|---|---|---|---|---|---|",
             f"| {pct(m['tp'], m['tp'] + m['fp'])} | {pct(m['tp'], m['tp'] + m['fn'])} | {m['low']:.0%}–{m['high']:.0%} | "
             f"{m['tp']} | {m['fp']} | {m['fn']} | {m['unknown']} |", ""]
    if m["wrong"]:
        lines += ["Referências erradas (quebram o portão de precisão):", ""] + [f"- {w}" for w in m["wrong"]] + [""]
    lines += ["| causa da perda | perdidas | parte |", "|---|---|---|"]
    ranked = sorted(m["causes"].items(), key=lambda kv: -len(kv[1]))
    lines += [f"| {label} | {len(items)} | {pct(len(items), m['fn'])} |" for label, items in ranked]
    lines += ["", "Exemplos:", ""]
    for label, items in ranked:
        lines += [f"- **{label}**"] + [f"  - {item}" for item in items[:show]]
    return lines + [""]


def summary(measures):
    groups = Counter()
    by_repo = defaultdict(Counter)
    for m in measures:
        for label, items in m["causes"].items():
            group = label.split(" (")[0]
            groups[group] += len(items)
            by_repo[group][m["repo"]] += len(items)
    total = sum(groups.values())
    repos = [m["repo"] for m in measures]
    lines = ["## Resumo das causas", "", "| causa | " + " | ".join(repos) + " | total | parte |",
             "|---|" + "---|" * (len(repos) + 2)]
    for group, count in groups.most_common():
        lines.append(f"| {group} | " + " | ".join(str(by_repo[group][r]) for r in repos) + f" | {count} | {pct(count, total)} |")
    return lines + [""]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repos", nargs="+")
    parser.add_argument("--set", default="", help="named sample from sample.py --set")
    parser.add_argument("--tool", default="mira")
    parser.add_argument("--show", type=int, default=3, help="examples per cause")
    args = parser.parse_args()
    rng = random.Random(SEED)
    measures = [measure(repo, args.set, args.tool, rng) for repo in args.repos]
    sizes = ", ".join(f"{m['repo']} {len(__import__('json').loads(sample_path(m['repo'], args.set).read_text())['symbols'])}" for m in measures)
    today = date.today().isoformat()
    head = [f"# Recall e causas das perdidas: {args.tool} ({today})", "",
            f"Amostra `{args.set or 'publicada'}` (semente {SEED}; símbolos sorteados: {sizes}). O intervalo de 95% "
            f"reamostra os símbolos {BOOTSTRAP} vezes. A causa vem do índice do Mira; a origem de um receiver sem tipo "
            "é classificada por padrão de texto nas linhas acima do uso, então confira pelos exemplos.", ""]
    body = summary(measures) + [line for m in measures for line in section(m, args.show)]
    out = BENCH / "results" / f"recall-{today}.md"
    out.write_text("\n".join(head + body))
    for m in measures:
        print(f"{m['repo']}: precision {pct(m['tp'], m['tp'] + m['fp'])}, recall {pct(m['tp'], m['tp'] + m['fn'])} "
              f"[{m['low']:.0%}–{m['high']:.0%}], {m['fn']} missed")
    print(f"written to {out}")
    if any(m["fp"] for m in measures):
        print("precision gate failed: wrong references above", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
