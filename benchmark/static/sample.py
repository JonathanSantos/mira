#!/usr/bin/env python3
"""Sorteia os símbolos medidos em cada repositório, a partir do gabarito.

O sorteio é estratificado por tipo de símbolo e metade de cada estrato tem
homônimos (o mesmo nome em outra definição), que é onde busca textual erra.
A semente é fixa: a amostra é a mesma para todas as ferramentas e rodadas.
Com --set, sai outra amostra (a semente leva o nome do conjunto) e --scale
multiplica cada estrato; a amostra publicada continua em <repo>.json.

Uso: python3 static/sample.py [--set recall --scale 8] <repo> [<repo> ...]
"""
import argparse
import random
from collections import Counter

from common import EXCLUDED_PATHS, Truth, sample_path, write_json

SEED = 20260914
MIN_REFS, MAX_REFS = 2, 200
STRATA = {
    "callable": ({"function", "method"}, 16),
    "type": ({"class", "struct", "interface", "type", "enum", "record"}, 10),
    "member": ({"field", "property", "attribute", "enum_member"}, 10),
    "value": ({"variable", "var", "const"}, 4),
}


def query_for(d):
    return f"{d['container']}.{d['name']}" if d.get("container") else d["name"]


def sample(repo, sample_set="", scale=1):
    truth = Truth(repo)
    rng = random.Random(f"{SEED}:{repo}:{sample_set}" if sample_set else f"{SEED}:{repo}")
    picked = []
    for stratum, (kinds, base) in STRATA.items():
        size = base * scale
        pool = [d for d in truth.defs.values()
                if d["kind"] in kinds and not EXCLUDED_PATHS.search(d["file"])
                and MIN_REFS <= len(truth.refs[d["id"]]) <= MAX_REFS]
        homonyms = sorted((d for d in pool if len(truth.by_name[d["name"]]) > 1), key=lambda d: d["id"])
        unique = sorted((d for d in pool if len(truth.by_name[d["name"]]) == 1), key=lambda d: d["id"])
        take_h = min(len(homonyms), size // 2)
        take_u = min(len(unique), size - take_h)
        take_h = min(len(homonyms), size - take_u)
        chosen = rng.sample(homonyms, take_h) + rng.sample(unique, take_u)
        for d in sorted(chosen, key=lambda d: d["id"]):
            picked.append({**{k: d.get(k) for k in ("id", "name", "kind", "container", "file", "line", "col")},
                           "stratum": stratum, "refs": len(truth.refs[d["id"]]),
                           "homonym": len(truth.by_name[d["name"]]) > 1, "query": query_for(d)})
    meta = {"repo": repo, "seed": SEED, "symbols": picked}
    if sample_set:
        meta.update({"set": sample_set, "scale": scale})
    write_json(sample_path(repo, sample_set), meta)
    strata = Counter(s["stratum"] for s in picked)
    homonym = sum(s["homonym"] for s in picked)
    print(f"{repo}: {len(picked)} symbols {dict(strata)}, {homonym} with homonyms, refs median "
          f"{sorted(s['refs'] for s in picked)[len(picked) // 2] if picked else 0}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repos", nargs="+")
    parser.add_argument("--set", default="", help="named sample, written next to the published one")
    parser.add_argument("--scale", type=int, default=1, help="multiplies every stratum size")
    args = parser.parse_args()
    for name in args.repos:
        sample(name, args.set, args.scale)


if __name__ == "__main__":
    main()
