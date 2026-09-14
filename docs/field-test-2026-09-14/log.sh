#!/bin/bash
# Uso: log.sh <arquivo-de-log> -- <comando...>
# Roda o comando, imprime a saída e registra comando + bytes no log.
LOG="$1"; shift; [ "$1" = "--" ] && shift
OUT=$("$@" 2>&1); STATUS=$?
BYTES=$(printf '%s' "$OUT" | wc -c | tr -d ' ')
{
  printf '### CMD: %q ' "$@"; printf '\n'
  printf '%s\n' "$OUT"
  printf '### STATUS: %s BYTES: %s\n\n' "$STATUS" "$BYTES"
} >> "$LOG"
printf '%s\n' "$OUT"
exit $STATUS
