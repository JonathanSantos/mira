#!/usr/bin/env python3
"""Clona os repositórios do benchmark em _repos/, cada um num commit fixo.

Na primeira execução, repositórios sem sha em repos.json são fixados no HEAD
atual e o resultado vai para repos.lock.json. As execuções seguintes usam o
lock, para toda medição rodar sobre o mesmo código.

Uso: python3 scripts/clone.py [nome ...]
"""
import json
import subprocess
import sys
from pathlib import Path

BENCH = Path(__file__).resolve().parent.parent
REPOS = BENCH / "_repos"


def git(*args, cwd=None):
    result = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)}: {result.stderr.strip()}")
    return result.stdout.strip()


def clone(name, url, sha):
    dest = REPOS / name
    if (dest / ".git").exists() and git("rev-parse", "HEAD", cwd=dest) == sha:
        print(f"{name}: already at {sha[:12]}", flush=True)
        return
    dest.mkdir(parents=True, exist_ok=True)
    if not (dest / ".git").exists():
        git("init", "-q", cwd=dest)
        git("remote", "add", "origin", url, cwd=dest)
    git("fetch", "-q", "--depth", "1", "origin", sha, cwd=dest)
    git("checkout", "-q", "--force", "FETCH_HEAD", cwd=dest)
    print(f"{name}: {sha[:12]}", flush=True)


def main():
    manifest = json.loads((BENCH / "repos.json").read_text())
    lock_path = BENCH / "repos.lock.json"
    lock = json.loads(lock_path.read_text()) if lock_path.exists() else {}
    only = set(sys.argv[1:])
    for repo in manifest["repos"]:
        name = repo["name"]
        if only and name not in only:
            continue
        sha = lock.get(name) or repo.get("sha") or git("ls-remote", repo["url"], "HEAD").split()[0]
        clone(name, repo["url"], sha)
        lock[name] = sha
        lock_path.write_text(json.dumps(lock, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
