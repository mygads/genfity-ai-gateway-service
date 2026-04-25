#!/usr/bin/env bash
set -euo pipefail

DATABASE_URL="${DATABASE_URL:-postgres://postgres:postgres@localhost:5432/genfity_ai_gateway?sslmode=disable}"

printf 'Using DATABASE_URL=%s\n' "$DATABASE_URL"

if ! command -v goose >/dev/null 2>&1; then
  printf 'goose not found. Install with: go install github.com/pressly/goose/v3/cmd/goose@latest\n' >&2
  exit 1
fi

if ! command -v psql >/dev/null 2>&1; then
  printf 'psql not found. Please install PostgreSQL client tools.\n' >&2
  exit 1
fi

DB_NAME="$(printf '%s' "$DATABASE_URL" | sed -E 's#^.*/([^/?]+).*#\1#')"
BASE_URL="$(printf '%s' "$DATABASE_URL" | sed -E 's#(postgres(ql)?://[^/]+)/.*#\1/postgres#')"

printf 'Ensuring database %s exists...\n' "$DB_NAME"
psql -tc "SELECT 1 FROM pg_database WHERE datname = '$DB_NAME'" "$BASE_URL" | grep -q 1 || psql -c "CREATE DATABASE \"$DB_NAME\"" "$BASE_URL"

printf 'Running migrations...\n'
goose -dir internal/database/migrations postgres "$DATABASE_URL" up

printf 'Done.\n'
