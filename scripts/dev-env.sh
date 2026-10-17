#!/usr/bin/env bash
#
# Creates apps/go-modular/.env from .env.example on first run, with a generated
# JWT secret.
#
# The API refuses to start without a secret, which is deliberate: a default one
# would be a known value in every deployment that forgot to change it. That
# strictness is worth keeping, so this fills the gap for development instead of
# loosening the check.
#
# An existing .env is never touched.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="$ROOT/apps/go-modular"
ENV_FILE="$APP_DIR/.env"
EXAMPLE="$APP_DIR/.env.example"

if [[ -f "$ENV_FILE" ]]; then
    echo "  .env already exists, leaving it alone"
    exit 0
fi

if [[ ! -f "$EXAMPLE" ]]; then
    echo "  no .env.example to copy; skipping" >&2
    exit 0
fi

if ! command -v openssl >/dev/null 2>&1; then
    echo "  openssl is needed to generate a JWT secret" >&2
    exit 1
fi

SECRET="$(openssl rand -hex 32)"

# Every other line is copied as-is; only the empty JWT_SECRET is filled in. The
# substitution is done here rather than with sed so the secret never appears in a
# process listing.
python3 - "$EXAMPLE" "$ENV_FILE" "$SECRET" <<'PYFILL'
import sys

example, target, secret = sys.argv[1], sys.argv[2], sys.argv[3]

with open(example, encoding='utf-8') as source:
    lines = source.read().splitlines()

out = []
filled = False
for line in lines:
    if line.startswith('JWT_SECRET=') and line.strip() == 'JWT_SECRET=':
        out.append(f'JWT_SECRET={secret}')
        filled = True
    else:
        out.append(line)

if not filled:
    out.append(f'JWT_SECRET={secret}')

with open(target, 'w', encoding='utf-8') as handle:
    handle.write('\n'.join(out) + '\n')

# The file holds a secret, so it is readable only by its owner.
import os
os.chmod(target, 0o600)
PYFILL

echo "  created .env with a generated JWT_SECRET"
