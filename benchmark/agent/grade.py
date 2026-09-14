#!/usr/bin/env python3
"""Corrige uma rodada de agente e junta o custo registrado pelo comando `b`.

impact: precision e recall das linhas citadas contra as sites da chave, e
    quantas citações caíram em homônimos.
rename: linhas que precisavam mudar e mudaram, linhas de homônimos que ficaram
    intactas, linhas alteradas fora da chave e, em Go, se `go build` e
    `go vet` passam na cópia.

Uso: python3 agent/grade.py <run-dir> [--tokens N] [--tool-uses N] [--duration-ms N]
"""
import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "static"))
from common import BENCH, manifest  # noqa: E402


def word(name):
    return re.compile(rf"(?<![\w$]){re.escape(name)}(?![\w$])")


def line_of(repo, site):
    file, _, number = site.rpartition(":")
    try:
        lines = (Path(repo) / file).read_text(errors="replace").split("\n")
    except OSError:
        return ""
    n = int(number)
    return lines[n - 1] if 0 < n <= len(lines) else ""


def grade_impact(run, task):
    try:
        cited = {s.strip() for s in json.loads((run / "answer.json").read_text())["sites"]}
    except (OSError, ValueError, KeyError, TypeError):
        return {"answer": "missing or invalid"}
    key, homonyms = set(task["key"]["sites"]), set(task["key"]["homonym_sites"])
    tp = cited & key
    # Medida tolerante, reportada ao lado da estrita: separa "achou o lugar
    # certo" de "informou a linha certa" (a Serena numera linhas a partir de 0).
    near_key = {k for k in key if any(shifted(k, d) in cited for d in (-1, 0, 1))}
    near_cited = {c for c in cited if any(shifted(c, d) in key for d in (-1, 0, 1))}
    return {"cited": len(cited), "tp": len(tp), "fp": len(cited - key), "fn": len(key - cited), "homonym_fp": len(cited & homonyms),
            "precision": round(len(tp) / len(cited), 3) if cited else 0.0, "recall": round(len(tp) / len(key), 3),
            "precision_within_1": round(len(near_cited) / len(cited), 3) if cited else 0.0,
            "recall_within_1": round(len(near_key) / len(key), 3)}


def shifted(site, delta):
    file, _, line = site.rpartition(":")
    return f"{file}:{int(line) + delta}" if line.isdigit() else site


def changed_lines(repo):
    diff = subprocess.run(["git", "-C", str(repo), "diff", "-U0", "--no-color"], capture_output=True, text=True).stdout
    changed, file = set(), None
    for line in diff.splitlines():
        if line.startswith("+++ b/"):
            file = line[6:]
        elif line.startswith("@@") and file:
            m = re.search(r"\+(\d+)(?:,(\d+))?", line)
            start, count = int(m.group(1)), int(m.group(2) or 1)
            changed |= {f"{file}:{n}" for n in range(start, start + count)}
    return changed


def grade_rename(run, task, lang):
    repo = run / "repo"
    old, new = word(task["key"]["old_name"]), word(task["key"]["new_name"])
    must_change, must_keep = task["key"]["must_change"], task["key"]["must_keep"]
    changed_ok = [s for s in must_change if new.search(line_of(repo, s))]
    kept_ok = [s for s in must_keep if old.search(line_of(repo, s)) and not new.search(line_of(repo, s))]
    collateral = changed_lines(repo) - set(must_change)
    result = {"changed": f"{len(changed_ok)}/{len(must_change)}", "kept": f"{len(kept_ok)}/{len(must_keep)}",
              "collateral_lines": len(collateral), "complete": len(changed_ok) == len(must_change) and len(kept_ok) == len(must_keep)}
    if lang == "go":
        build = subprocess.run("go build ./... && go vet ./...", shell=True, cwd=repo, capture_output=True, text=True)
        result["compiles"] = build.returncode == 0
        result["compile_errors"] = build.stderr.strip().splitlines()[:5]
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run")
    parser.add_argument("--tokens", type=int)
    parser.add_argument("--tool-uses", type=int)
    parser.add_argument("--duration-ms", type=int)
    args = parser.parse_args()
    run = Path(args.run)
    meta = json.loads((run / "meta.json").read_text())
    repo_name = meta["task"].rsplit("-", 1)[0]
    task = next(t for t in json.loads((BENCH / "agent" / "tasks" / f"{repo_name}.json").read_text())["tasks"] if t["id"] == meta["task"])
    calls = [json.loads(l) for l in (run / "calls.jsonl").read_text().splitlines()] if (run / "calls.jsonl").exists() else []
    result = {**meta, "calls": len(calls), "bytes_read": sum(c["bytes"] for c in calls), "tool_ms": sum(c["ms"] for c in calls),
              "tokens": args.tokens, "tool_uses": args.tool_uses, "duration_ms": args.duration_ms}
    if task["kind"] == "impact":
        result.update(grade_impact(run, task))
    else:
        result.update(grade_rename(run, task, manifest()[repo_name]["lang"]))
    (run / "result.json").write_text(json.dumps(result, indent=2))
    print(json.dumps({k: v for k, v in result.items() if k not in ("repo", "socket", "compile_errors")}))


if __name__ == "__main__":
    main()
