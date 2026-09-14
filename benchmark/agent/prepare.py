#!/usr/bin/env python3
"""Prepara uma rodada de agente e imprime o prompt a enviar ao subagente.

Cria _data/agent/runs/<task>__<arm>__<model>__r<rep>/ com:
  repo/      cópia do repositório (só em tarefas de edição; leitura usa o clone)
  bin/       atalhos das ferramentas do braço
  b          o único comando que o agente usa: registra cada chamada em calls.jsonl
  prompt.md  regras, guia da ferramenta e a tarefa
  meta.json  tarefa, braço, modelo, repetição e caminhos

Uso: python3 agent/prepare.py <task-id> <arm> [--model haiku] [--rep 1]
"""
import argparse
import hashlib
import json
import shutil
import stat
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "static"))
from common import BENCH, DATA, repo_path  # noqa: E402

TOOLS = BENCH / "_tools"
READ = ["sed", "cat", "ls", "head", "wc"]
ARMS = {
    "baseline": ["rg", "find", *READ],
    "mira": ["mira", *READ],
    "serena": ["serena-call", *READ],
    "probe": ["probe", *READ],
    "aider-map": READ,
}
GUIDES = {
    "baseline": ("Search with ripgrep and read files with sed or cat.\n"
                 "- `rg -n -w NAME .` finds whole-word matches (always pass a path such as `.`)\n"
                 "- `sed -n '120,160p' path/to/file` prints a line range\n"
                 "- `find . -name '*.go'` lists files"),
    "probe": ("Probe searches code without an index and returns whole code blocks.\n"
              "- `probe search \"QUERY\" . --allow-tests [--exact] [--max-results N]` keyword search (AND/OR syntax)\n"
              "- `probe extract path/to/file:LINE` or `path/to/file#Symbol` prints the enclosing block\n"
              "- `probe query 'AST_PATTERN' . -l LANGUAGE` structural search (ast-grep patterns)\n"
              "- `probe symbols path/to/file` lists the symbols of a file\n"
              "Read other lines with `sed -n 'A,Bp' file`."),
    "serena": ("Serena answers through a language server. Call its tools with JSON arguments:\n"
               "`serena-call TOOL '{\"arg\": \"value\"}'`. Useful tools: find_symbol (name_path_pattern, relative_path, include_body), "
               "find_referencing_symbols (name_path, relative_path), get_symbols_overview (relative_path), "
               "search_for_pattern (substring_pattern, relative_path), rename_symbol (name_path, relative_path, new_name), "
               "replace_symbol_body, insert_after_symbol, find_declaration, find_implementations.\n"
               "Line numbers in Serena's answers start at 0. Read files with `sed -n 'A,Bp' file`.\n\n"
               "Serena's own instructions:\n{instructions}"),
    "aider-map": ("Below is aider's repository map (4096 tokens): the most relevant files and their key definitions. "
                  "You cannot search; read files with `sed -n 'A,Bp' file` or `cat file`.\n\n{repo_map}"),
}


def executable(path, body):
    path.write_text(body)
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("task")
    parser.add_argument("arm", choices=sorted(ARMS))
    parser.add_argument("--model", default="haiku")
    parser.add_argument("--rep", type=int, default=1)
    args = parser.parse_args()
    repo_name = args.task.rsplit("-", 1)[0]
    task = next(t for t in json.loads((BENCH / "agent" / "tasks" / f"{repo_name}.json").read_text())["tasks"] if t["id"] == args.task)
    run = DATA / "agent" / "runs" / f"{args.task}__{args.arm}__{args.model}__r{args.rep}"
    if run.exists():
        shutil.rmtree(run)
    (run / "bin").mkdir(parents=True)
    repo = repo_path(repo_name)
    # Edições e o braço Serena trabalham numa cópia: a edição não suja o clone,
    # e duas Serenas no mesmo projeto disputariam o .serena/cache.
    if task["kind"] == "rename" or args.arm == "serena":
        subprocess.run(["cp", "-R", str(repo), str(run / "repo")], check=True)
        repo = run / "repo"
    bin_dir = run / "bin"
    executable(bin_dir / "rg", f'#!/bin/bash\nexec -a rg "{Path.home() / ".local" / "bin" / "claude"}" "$@"\n')
    executable(bin_dir / "mira", f'#!/bin/sh\nexec "{TOOLS / "bin" / "mira"}" "$@"\n')
    executable(bin_dir / "probe", f'#!/bin/sh\nexec "{TOOLS / "npm" / "bin" / "probe"}" "$@"\n')
    # Sockets Unix no macOS aceitam no máximo 104 bytes de caminho: um hash curto no lugar do nome da rodada.
    socket = DATA / "sockets" / f"{hashlib.sha1(run.name.encode()).hexdigest()[:10]}.sock"
    executable(bin_dir / "serena-call", f'#!/bin/sh\nexec "{TOOLS / "bin" / "mcpcall"}" call -socket "{socket}" "$@"\n')
    allowed = ",".join(ARMS[args.arm])
    executable(run / "b", "#!/bin/sh\n"
               f'export BENCH_RUN_LOG="{run / "calls.jsonl"}" BENCH_REPO="{repo}" BENCH_ALLOWED="{allowed}" PATH="{bin_dir}:$PATH"\n'
               f'exec "{BENCH / "agent" / "bench"}" "$@"\n')
    guide = GUIDES.get(args.arm, "")
    if args.arm == "mira":
        guide = (BENCH.parent / "internal" / "skill" / "SKILL.md").read_text()
    if args.arm == "serena":
        cached = DATA / "agent" / "serena-server-instructions.txt"  # instruções do initialize, iguais para todo projeto
        guide = guide.replace("{instructions}", cached.read_text() if cached.exists() else "(not available)")
    if args.arm == "aider-map":
        guide = guide.replace("{repo_map}", (DATA / "repomap" / f"{repo_name}-4096.txt").read_text(errors="replace"))
    edit_rule = "- Change files with the Edit tool; everything you read or search still goes through the run command.\n" if task["kind"] == "rename" else ""
    answer_rule = f"\nWrite the answer file with the Write tool at: {run / 'answer.json'}\n" if task["kind"] == "impact" else ""
    prompt = (f"# Benchmark run {run.name}\n\n"
              f"You are working on the repository at {repo}. You are measured on correctness and on how much you read.\n\n"
              "## Rules\n"
              f"- Run every shell command through the run command: `{run / 'b'} COMMAND [ARGS...]`. It runs in the repository root.\n"
              f"- Available commands: {', '.join(ARMS[args.arm])}. Do not use the Read, Grep or Glob tools, and no shell command without the run command.\n"
              f"{edit_rule}"
              "- Paths are relative to the repository root.\n\n"
              f"## Tool guide\n\n{guide}\n\n"
              f"## Task\n\n{task['prompt']}\n{answer_rule}")
    (run / "prompt.md").write_text(prompt)
    (run / "meta.json").write_text(json.dumps({"task": args.task, "arm": args.arm, "model": args.model, "rep": args.rep,
                                               "repo": str(repo), "socket": str(socket), "prompt_bytes": len(prompt.encode())}, indent=2))
    print(run / "prompt.md")


if __name__ == "__main__":
    main()
