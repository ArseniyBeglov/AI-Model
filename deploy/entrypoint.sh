#!/bin/sh
set -eu

if [ "$#" -eq 0 ]; then
  exec tail -f /dev/null
fi

exec /app/ai-model "$@"
