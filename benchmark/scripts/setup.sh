#!/usr/bin/env bash
# Instala tudo que o benchmark usa em benchmark/_tools, sem tocar no sistema.
# Pré-requisitos: git, go, node/npm e python3. Uso: bash benchmark/scripts/setup.sh
# WITH_JDK17=1 baixa também um JDK 17 (Adoptium) para o import Gradle do
# spring-petclinic na Serena; sem ele o servidor Java não acha símbolos.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=env.sh
source "$here/env.sh"
root="$(cd "$BENCH/.." && pwd)"
export SERENA_HOME="$BENCH/_tools/serena-home"
mkdir -p "$BENCH/_tools/bin" "$SERENA_HOME" "$BENCH/_data/logs"

step() { printf '\n== %s\n' "$1"; }

step "repositórios nos commits fixos"
python3 "$BENCH/scripts/clone.py"

step "uv isolado"
[ -x "$BENCH/_tools/bootstrap/bin/uv" ] || { python3 -m venv "$BENCH/_tools/bootstrap"; "$BENCH/_tools/bootstrap/bin/pip" install -q uv; }

step "mira e ponte MCP"
(cd "$root" && go build -o "$BENCH/_tools/bin/mira" ./cmd/mira && go build -o "$BENCH/_tools/bin/mcpcall" ./benchmark/cmd/mcpcall)

step "Serena 1.7.0"
uv tool install -q -p 3.13 serena-agent==1.7.0

step "aider 0.86.2"
uv tool install -q --python 3.12 --with pip aider-chat==0.86.2

step "Probe 0.6.0-rc339"
npm install -g --no-audit --no-fund @probelabs/probe@0.6.0-rc339

step "gabaritos: Go (go/types), TypeScript (tsc 6), Python (jedi)"
(cd "$BENCH/groundtruth/gotruth" && go build -o "$BENCH/_tools/bin/gotruth" .)
(cd "$BENCH/groundtruth/tstruth" && npm install --no-audit --no-fund)
[ -x "$BENCH/_tools/py312/bin/python" ] || uv venv -q --python 3.12 "$BENCH/_tools/py312"
uv pip install -q --python "$BENCH/_tools/py312/bin/python" jedi

if [ "${WITH_JDK17:-0}" = 1 ]; then
  step "JDK 17 para o import Gradle da Serena"
  mkdir -p "$BENCH/_tools/jdk17" "$BENCH/_tools/gradle-home"
  if ! find "$BENCH/_tools/jdk17" -maxdepth 3 -path "*/Contents/Home" -type d | grep -q .; then
    curl -fsSL -o "$BENCH/_tools/jdk17/jdk17.tar.gz" "https://api.adoptium.net/v3/binary/latest/17/ga/mac/aarch64/jdk/hotspot/normal/eclipse"
    tar -xzf "$BENCH/_tools/jdk17/jdk17.tar.gz" -C "$BENCH/_tools/jdk17" && rm "$BENCH/_tools/jdk17/jdk17.tar.gz"
  fi
  echo "set ls_specific_settings.java.gradle_java_home in $SERENA_HOME/serena_config.yml to:"
  find "$BENCH/_tools/jdk17" -maxdepth 3 -path "*/Contents/Home" -type d | head -1
fi

step "pronto"
echo "Gabaritos: veja benchmark/README.md. A primeira execução da Serena em Java baixa o servidor e um JRE 21 (~360 MB)."
