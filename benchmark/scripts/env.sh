# Caminhos isolados do benchmark: ferramentas, Pythons e caches ficam em
# benchmark/_tools, nada é instalado no sistema. Uso: source benchmark/scripts/env.sh
BENCH="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
export BENCH
export UV_CACHE_DIR="$BENCH/_tools/uv-cache"
export UV_PYTHON_INSTALL_DIR="$BENCH/_tools/python"
export UV_TOOL_DIR="$BENCH/_tools/uv-tools"
export UV_TOOL_BIN_DIR="$BENCH/_tools/bin"
export npm_config_prefix="$BENCH/_tools/npm"
export PATH="$BENCH/_tools/bin:$BENCH/_tools/npm/bin:$BENCH/_tools/bootstrap/bin:$PATH"
