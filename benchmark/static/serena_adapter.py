"""Adaptador da Serena: o servidor MCP fica vivo durante a rodada, atrás da
ponte mcpcall, e cada pergunta vira chamadas às tools da própria Serena.

Referências custam duas chamadas, como para um agente: find_symbol para achar
o name_path exato da definição e find_referencing_symbols com ele. Bytes e
latência somam as duas. As linhas da Serena começam em zero e são convertidas.
"""
import json
import os
import re
import subprocess
import time

from adapters import Answer, timed
from common import BENCH, DATA, manifest, repo_path

TOOLS = BENCH / "_tools"
REFERENCE_LINE = re.compile(r"^\s*>\s*(\d+):", re.MULTILINE)


def serena_env():
    env = dict(os.environ)
    env["SERENA_HOME"] = str(TOOLS / "serena-home")
    env["PATH"] = os.pathsep.join([str(TOOLS / "bin"), str(TOOLS / "npm" / "bin"), str(TOOLS / "bootstrap" / "bin"), env.get("PATH", "")])
    for key, sub in (("UV_CACHE_DIR", "uv-cache"), ("UV_PYTHON_INSTALL_DIR", "python"), ("UV_TOOL_DIR", "uv-tools"), ("UV_TOOL_BIN_DIR", "bin")):
        env[key] = str(TOOLS / sub)
    return env


class Serena:
    name = "serena"

    def __init__(self, repo):
        self.repo = repo
        self.lang = manifest()[repo]["lang"]
        self.socket = DATA / "sockets" / f"serena-{repo}.sock"
        self.log = DATA / "logs" / f"serena-{repo}-calls.jsonl"
        self.env = serena_env()
        self.server = None
        self.startup_ms = 0.0

    def __enter__(self):
        self.socket.parent.mkdir(parents=True, exist_ok=True)
        self.log.parent.mkdir(parents=True, exist_ok=True)
        server_log = open(DATA / "logs" / f"serena-{self.repo}-server.log", "w")
        started = time.perf_counter()
        self.server = subprocess.Popen(
            [str(TOOLS / "bin" / "mcpcall"), "serve", "-socket", str(self.socket), "-log", str(self.log), "--",
             "serena", "start-mcp-server", "--project", str(repo_path(self.repo)), "--context", "agent", "--transport", "stdio",
             "--enable-web-dashboard", "false", "--open-web-dashboard", "false", "--enable-gui-log-window", "false"],
            env=self.env, stdout=server_log, stderr=subprocess.STDOUT)
        self._call("get_current_config", {})  # espera o servidor aceitar chamadas
        self.startup_ms = (time.perf_counter() - started) * 1000
        return self

    def __exit__(self, *exc):
        subprocess.run([str(TOOLS / "bin" / "mcpcall"), "stop", "-socket", str(self.socket)], env=self.env, capture_output=True)
        if self.server:
            try:
                self.server.wait(timeout=60)
            except subprocess.TimeoutExpired:
                self.server.kill()
        return False

    def _call(self, tool, args):
        proc, ms = timed([str(TOOLS / "bin" / "mcpcall"), "call", "-socket", str(self.socket), tool, json.dumps(args)], timeout=600)
        if proc is None:
            return "", ms, "timeout"
        error = "" if proc.returncode == 0 else (proc.stdout.strip()[:300] or proc.stderr.strip()[:300])
        return proc.stdout, ms, error

    def _pattern(self, symbol):
        # Métodos Go não levam o receptor no name_path da Serena.
        if symbol.get("container") and self.lang != "go":
            return f"{symbol['container']}/{symbol['name']}"
        return symbol["name"]

    @staticmethod
    def _parse_symbols(text):
        try:
            data = json.loads(text)
        except json.JSONDecodeError:
            return []
        return data if isinstance(data, list) else []

    def definition(self, symbol):
        text, ms, error = self._call("find_symbol", {"name_path_pattern": self._pattern(symbol)})
        lines = [(s["relative_path"], s["body_location"]["start_line"] + 1, s["body_location"]["end_line"] + 1)
                 for s in self._parse_symbols(text) if "body_location" in s]
        return Answer(lines=lines, agent_bytes=len(text.encode()), ms=ms, error=error)

    def references(self, symbol):
        text, ms, error = self._call("find_symbol", {"name_path_pattern": symbol["name"], "relative_path": symbol["file"]})
        candidates = [s for s in self._parse_symbols(text)
                      if s.get("relative_path") == symbol["file"]
                      and s["body_location"]["start_line"] + 1 <= symbol["line"] <= s["body_location"]["end_line"] + 1]
        if not candidates:
            return Answer(agent_bytes=len(text.encode()), ms=ms, error=error or "definition not found")
        target = min(candidates, key=lambda s: s["body_location"]["end_line"] - s["body_location"]["start_line"])
        refs_text, refs_ms, refs_error = self._call("find_referencing_symbols",
                                                    {"name_path": target["name_path"], "relative_path": symbol["file"]})
        lines = []
        try:
            data = json.loads(refs_text)
        except json.JSONDecodeError:
            data = {}
        for file, kinds in (data.items() if isinstance(data, dict) else []):
            for entries in kinds.values():
                for entry in entries:
                    lines += [(file, int(n) + 1) for n in REFERENCE_LINE.findall(entry.get("content_around_reference", ""))]
        return Answer(lines=lines, agent_bytes=len(text.encode()) + len(refs_text.encode()), ms=ms + refs_ms,
                      error=refs_error or ("" if data or refs_text.strip() == "{}" else "unparsed output"))
