#!/usr/bin/env python3
"""Gera os gráficos SVG do README a partir dos resultados publicados.

Lê results/static-<data>.md e results/agent-<data>.md (gerados por static/score.py
e agent/report.py) e results/reindex-<data>.json, e grava em docs/assets uma
versão clara e uma escura de cada gráfico: o <picture> do README escolhe pela
preferência de tema do leitor. Só biblioteca padrão.

Uso: python3 benchmark/charts.py
"""
import json
import math
from pathlib import Path

BENCH = Path(__file__).resolve().parent
ASSETS = BENCH.parent / "docs" / "assets"
DATE = "2026-09-14"
FONT = "-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif"

THEMES = {
    "light": {"bg": "#ffffff", "text": "#1f2328", "muted": "#656d76", "grid": "#d8dee4", "mira": "#7c3aed",
              "serena": "#0284c7", "grep": "#8c959f", "baseline": "#8c959f", "probe": "#d97706", "aider-map": "#059669",
              "before": "#c6ccd2"},
    "dark": {"bg": "#0d1117", "text": "#e6edf3", "muted": "#8b949e", "grid": "#30363d", "mira": "#a78bfa",
             "serena": "#38bdf8", "grep": "#6e7781", "baseline": "#6e7781", "probe": "#fbbf24", "aider-map": "#34d399",
             "before": "#484f58"},
}
LABELS = {"mira": "Mira", "serena": "Serena", "grep": "grep", "baseline": "rg + sed", "probe": "Probe",
          "aider-map": "aider repo map"}
REPOS = [("gin", ("gin", "Go")), ("flask", ("flask", "Python")), ("spring-petclinic", ("petclinic", "Java")),
         ("excalidraw", ("excalidraw", "TypeScript")), ("react-hook-form", ("react-hook-form", "TypeScript"))]
TASKS = [("gin-rename", ("gin", "rename Context.Param")), ("spring-petclinic-impact", ("petclinic", "callers of Owner.getPet")),
         ("excalidraw-impact", ("excalidraw", "callers of StoreChange.create")),
         ("flask-impact", ("flask", "callers of JSONProvider.dumps"))]


def esc(text):
    return str(text).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;").replace('"', "&quot;")


def number(cell):
    try:
        return float(cell.rstrip("%").replace(",", "."))
    except ValueError:
        return None


def static_tables():
    """(repo, seção, ferramenta) -> células da linha, das tabelas de score.py."""
    out, repo, section = {}, None, None
    for line in (BENCH / "results" / f"static-{DATE}.md").read_text().splitlines():
        if line.startswith("### "):
            repo = line[4:].strip()
        elif line.startswith("Referências"):
            section = "refs"
        elif line.startswith("Definição"):
            section = "def"
        elif line.startswith("| ") and not line.startswith(("| ferramenta", "|---")):
            cells = [c.strip() for c in line.strip("|").split("|")]
            out[(repo, section, cells[0])] = cells[1:]
    return out


def agent_calls():
    """(tarefa, braço) -> chamadas da repetição mais recente."""
    out, task = {}, None
    for line in (BENCH / "results" / f"agent-{DATE}.md").read_text().splitlines():
        if line.startswith("## "):
            task = line[3:].split(" ")[0]
        elif line.startswith("| ") and not line.startswith(("| braço", "|---")):
            cells = [c.strip() for c in line.strip("|").split("|")]
            arm, rep, calls = cells[0], int(cells[1]), float(cells[3])
            if (task, arm) not in out or rep > out[(task, arm)][0]:
                out[(task, arm)] = (rep, calls)
    return {k: v[1] for k, v in out.items()}


def linear(top):
    return lambda v: v / top


def logarithmic(lo, hi):
    return lambda v: (math.log10(max(v, lo)) - math.log10(lo)) / (math.log10(hi) - math.log10(lo))


def document(width, height, theme, title, subtitle, body):
    return (f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}" '
            f'role="img" aria-label="{esc(title)}"><title>{esc(title)}</title>'
            f'<rect width="{width}" height="{height}" rx="10" fill="{theme["bg"]}"/>'
            f'<g font-family="{FONT}">'
            f'<text x="24" y="34" font-size="17" font-weight="600" fill="{theme["text"]}">{esc(title)}</text>'
            f'<text x="24" y="56" font-size="12" fill="{theme["muted"]}">{esc(subtitle)}</text>'
            f'{body}</g></svg>\n')


def legend(x, y, series, theme, names=None):
    """Legenda em linha; names troca o rótulo de uma série só neste gráfico."""
    parts = []
    for s in series:
        name = (names or {}).get(s, LABELS.get(s, s))
        parts.append(f'<rect x="{x:.0f}" y="{y - 10}" width="11" height="11" rx="2" fill="{theme[s]}"/>'
                     f'<text x="{x + 16:.0f}" y="{y}" font-size="12" fill="{theme["text"]}">{esc(name)}</text>')
        x += 16 + 7 * len(name) + 20
    return "".join(parts)


def grouped(x0, y0, w, h, groups, series, value, theme, scale, ticks, label=None):
    """Barras agrupadas: um grupo por item de groups (rótulo em duas linhas), uma barra por série."""
    parts = []
    for tick, text in ticks:
        y = y0 + h - scale(tick) * h
        parts.append(f'<line x1="{x0}" y1="{y:.1f}" x2="{x0 + w}" y2="{y:.1f}" stroke="{theme["grid"]}"/>'
                     f'<text x="{x0 - 8}" y="{y + 4:.1f}" font-size="11" text-anchor="end" fill="{theme["muted"]}">{text}</text>')
    gw = w / len(groups)
    bw = min(22, (gw - 18) / len(series))
    for gi, (key, (name, sub)) in enumerate(groups):
        left = x0 + gi * gw + (gw - bw * len(series)) / 2
        for si, s in enumerate(series):
            v = value(key, s)
            if v is None:
                continue
            bh = max(scale(v) * h, 1.5)
            x = left + si * bw
            parts.append(f'<rect x="{x:.1f}" y="{y0 + h - bh:.1f}" width="{bw - 3:.1f}" height="{bh:.1f}" rx="2" fill="{theme[s]}">'
                         f'<title>{esc(LABELS.get(s, s))}: {esc(label(v) if label else v)}</title></rect>')
            if label:
                parts.append(f'<text x="{x + (bw - 3) / 2:.1f}" y="{y0 + h - bh - 5:.1f}" font-size="10" text-anchor="middle" '
                             f'fill="{theme["muted"]}">{esc(label(v))}</text>')
        center = x0 + gi * gw + gw / 2
        size = 12 if gw >= 100 else 10.5  # grupos estreitos: "excalidraw" e "react-hook-form" não se encostam
        parts.append(f'<text x="{center:.1f}" y="{y0 + h + 19:.1f}" font-size="{size}" text-anchor="middle" fill="{theme["text"]}">{esc(name)}</text>'
                     f'<text x="{center:.1f}" y="{y0 + h + 34:.1f}" font-size="{size - 1}" text-anchor="middle" fill="{theme["muted"]}">{esc(sub)}</text>')
    return "".join(parts)


PERCENT_TICKS = [(0, "0%"), (25, "25%"), (50, "50%"), (75, "75%"), (100, "100%")]


def references_chart(theme, tables):
    series = ["mira", "serena", "grep", "probe"]
    body = legend(24, 86, series, theme)
    for i, (column, name) in enumerate([(0, "Precision"), (1, "Recall")]):
        x0 = 70 + i * 460
        body += f'<text x="{x0}" y="118" font-size="13" font-weight="600" fill="{theme["text"]}">{name}</text>'
        body += grouped(x0, 132, 400, 210, REPOS, series,
                        lambda repo, tool, c=column: number(tables.get((repo, "refs", tool), [None] * 10)[c] or ""),
                        theme, linear(100), PERCENT_TICKS)
    return document(960, 400, theme, "Find every use of a symbol, and only its uses",
                    "Reference precision and recall against compiler ground truth: 40 sampled symbols per repository (31 in petclinic)", body)


def definition_chart(theme, tables):
    series = ["mira", "serena", "probe", "grep", "aider-map"]
    body = legend(24, 86, series, theme)
    body += grouped(70, 112, 860, 220, REPOS, series,
                    lambda repo, tool: number(tables.get((repo, "def", tool), [""])[0] or ""),
                    theme, linear(100), PERCENT_TICKS, label=lambda v: f"{v:.0f}")
    return document(960, 390, theme, "Jump to the definition on the first try",
                    "Share of sampled symbols whose first candidate is the real definition", body)


def latency_chart(theme, tables):
    series = ["mira", "grep", "serena", "probe"]
    ticks = [(1, "1 ms"), (10, "10 ms"), (100, "100 ms"), (1000, "1 s")]
    body = legend(24, 86, series, theme)
    body += grouped(70, 112, 860, 220, REPOS, series,
                    lambda repo, tool: number(tables.get((repo, "refs", tool), [""] * 10)[8] or ""),
                    theme, logarithmic(1, 3000), ticks, label=lambda v: f"{v:.0f}")
    return document(960, 390, theme, "Answer in milliseconds",
                    "Median latency of a reference query, log scale. Serena answers through a running language server", body)


def agent_chart(theme, calls):
    series = ["mira", "serena", "baseline", "probe", "aider-map"]
    body = legend(24, 86, series, theme)
    body += grouped(70, 112, 860, 220, TASKS, series, lambda task, arm: calls.get((task, arm)), theme,
                    logarithmic(0.7, 200), [(1, "1"), (10, "10"), (100, "100")], label=lambda v: f"{v:.0f}")
    return document(960, 390, theme, "Fewer steps for an AI agent",
                    "Tool calls a Claude Haiku agent needed to finish each task, one run per cell, log scale", body)


def reindex_chart(theme, data):
    scale = logarithmic(0.01, 300)
    x0, w, y0, row = 290, 620, 104, 44
    body = legend(24, 86, ["before", "mira"], theme, {"before": "before the fixes", "mira": "Mira now"})
    for tick, text in [(0.01, "10 ms"), (0.1, "100 ms"), (1, "1 s"), (10, "10 s"), (100, "100 s")]:
        x = x0 + scale(tick) * w
        body += (f'<line x1="{x:.1f}" y1="{y0}" x2="{x:.1f}" y2="{y0 + row * len(data["scenarios"])}" stroke="{theme["grid"]}"/>'
                 f'<text x="{x:.1f}" y="{y0 + row * len(data["scenarios"]) + 16}" font-size="11" text-anchor="middle" fill="{theme["muted"]}">{text}</text>')
    for i, s in enumerate(data["scenarios"]):
        y = y0 + i * row + 8
        body += f'<text x="{x0 - 12}" y="{y + 16}" font-size="12" text-anchor="end" fill="{theme["text"]}">{esc(s["name"])}</text>'
        for j, (key, color) in enumerate([("before_s", "before"), ("after_s", "mira")]):
            bw = max(scale(s[key]) * w, 2)
            by = y + j * 14
            body += (f'<rect x="{x0}" y="{by}" width="{bw:.1f}" height="12" rx="2" fill="{theme[color]}"/>'
                     f'<text x="{x0 + bw + 6:.1f}" y="{by + 10}" font-size="10" fill="{theme["muted"]}">{duration(s[key])}</text>')
    return document(960, 400, theme, "Edits show up in the index right away",
                    f"Indexing and refresh time on {data['repo']} ({data['files']} files), before and after the fixes, log scale", body)


def duration(seconds):
    if seconds < 1:
        return f"{seconds * 1000:.0f} ms"
    if seconds < 60:
        return f"{seconds:.1f} s"
    return f"{int(seconds // 60)} min {seconds % 60:.0f} s"


def main():
    tables, calls = static_tables(), agent_calls()
    reindex = json.loads((BENCH / "results" / f"reindex-{DATE}.json").read_text())
    charts = {
        "references": lambda t: references_chart(t, tables),
        "definition": lambda t: definition_chart(t, tables),
        "latency": lambda t: latency_chart(t, tables),
        "agents": lambda t: agent_chart(t, calls),
        "reindex": lambda t: reindex_chart(t, reindex),
    }
    ASSETS.mkdir(parents=True, exist_ok=True)
    for name, build in charts.items():
        for theme_name, theme in THEMES.items():
            path = ASSETS / f"{name}-{theme_name}.svg"
            path.write_text(build(theme))
            print(path.relative_to(BENCH.parent))


if __name__ == "__main__":
    main()
