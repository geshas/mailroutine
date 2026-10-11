#!/bin/sh
# Usage: scripts/setenv.sh KEY VALUE [FILE]
# Sets KEY=VALUE in an env file (default .env), replacing an existing line or appending one.
# The value is written verbatim, so quote it yourself when needed (e.g. "'\$argon2id\$...'").
set -eu
key=$1
value=$2
file=${3:-.env}
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
touch "$file"
if grep -Eq "^#? *$key=" "$file"; then
  # Replace the first (possibly commented) occurrence; keep the rest untouched.
  awk -v k="$key" -v v="$value" '
    !done && $0 ~ ("^#? *" k "=") { print k "=" v; done = 1; next }
    { print }' "$file" >"$tmp"
else
  cp "$file" "$tmp"
  printf '%s=%s\n' "$key" "$value" >>"$tmp"
fi
cat "$tmp" >"$file"
