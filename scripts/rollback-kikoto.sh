#!/bin/sh
set -eu

# Restore a Kikoto deployment from a normalized release backup. The backup
# directory is expected to contain kikoto.db, .env, docker-compose.yml and a
# SHA256SUMS manifest covering those three files.

usage() {
  cat >&2 <<'EOF'
Usage:
  scripts/rollback-kikoto.sh --backup-dir DIR --image TAG --expected-schema N [--timeout SECONDS]

Required:
  --backup-dir DIR       Directory containing kikoto.db, .env,
                         docker-compose.yml and SHA256SUMS.
  --image TAG            Image to start after the files are restored.
  --expected-schema N    Expected schema_state.current_version.

Optional:
  --timeout SECONDS      Health-check timeout (default: 600).

Environment:
  KIKOTO_DEPLOY_DIR      Compose project directory (default: current directory).
  KIKOTO_CONFIG_DIR      Host config directory (default: DEPLOY_DIR/config).
  KIKOTO_DB_PATH         Host database path (default: CONFIG_DIR/kikoto.db).
  KIKOTO_ENV_FILE        Host .env path (default: DEPLOY_DIR/.env).
  KIKOTO_COMPOSE_FILE    Compose file path (default: DEPLOY_DIR/docker-compose.yml).
  KIKOTO_HEALTH_URL      Health URL (default: http://127.0.0.1:7655/health).
  KIKOTO_HEALTH_SCHEME   Scheme for the default health URL (default: http).
  KIKOTO_HEALTH_HOST     Host for the default health URL (default: 127.0.0.1).
  KIKOTO_HEALTH_PORT     Port for the default health URL (default: 7655).
  KIKOTO_COMPOSE_SERVICE Service to recreate (default: every service).

The script stops the Compose service, restores the three files, starts the
requested image, waits for GET /health to return 200, and verifies the SQLite
schema, quick_check and foreign_key_check. It never deletes the previous
database: that file is moved beside the restored one and a safety copy of the
current files is kept under DEPLOY_DIR/rollback-previous-<UTC>.

Exit codes: 0 success, 1 rollback failure, 2 usage error.
EOF
}

BACKUP_DIR=""
IMAGE=""
EXPECTED_SCHEMA=""
TIMEOUT=600

while [ "$#" -gt 0 ]; do
  case "$1" in
    --backup-dir)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      BACKUP_DIR=$2
      shift 2
      ;;
    --image)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      IMAGE=$2
      shift 2
      ;;
    --expected-schema)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      EXPECTED_SCHEMA=$2
      shift 2
      ;;
    --timeout)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      TIMEOUT=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'rollback-kikoto: unknown argument: %s\n' "$1" >&2
      usage
      exit 2
      ;;
  esac
done

if [ -z "$BACKUP_DIR" ] || [ -z "$IMAGE" ] || [ -z "$EXPECTED_SCHEMA" ]; then
  usage
  exit 2
fi
case "$EXPECTED_SCHEMA" in
  ''|*[!0-9]*)
    printf 'rollback-kikoto: --expected-schema must be a positive integer\n' >&2
    exit 2
    ;;
esac
if [ "$EXPECTED_SCHEMA" -le 0 ]; then
  printf 'rollback-kikoto: --expected-schema must be a positive integer\n' >&2
  exit 2
fi
case "$TIMEOUT" in
  ''|*[!0-9]*)
    printf 'rollback-kikoto: --timeout must be a positive integer\n' >&2
    exit 2
    ;;
esac
if [ "$TIMEOUT" -le 0 ]; then
  printf 'rollback-kikoto: --timeout must be a positive integer\n' >&2
  exit 2
fi

DEPLOY_DIR=${KIKOTO_DEPLOY_DIR:-$(pwd)}
CONFIG_DIR=${KIKOTO_CONFIG_DIR:-"$DEPLOY_DIR/config"}
DB_PATH=${KIKOTO_DB_PATH:-"$CONFIG_DIR/kikoto.db"}
ENV_FILE=${KIKOTO_ENV_FILE:-"$DEPLOY_DIR/.env"}
COMPOSE_FILE=${KIKOTO_COMPOSE_FILE:-"$DEPLOY_DIR/docker-compose.yml"}
HEALTH_SCHEME=${KIKOTO_HEALTH_SCHEME:-http}
HEALTH_HOST=${KIKOTO_HEALTH_HOST:-127.0.0.1}
HEALTH_PORT=${KIKOTO_HEALTH_PORT:-7655}
HEALTH_URL=${KIKOTO_HEALTH_URL:-"$HEALTH_SCHEME://$HEALTH_HOST:$HEALTH_PORT/health"}

COMPLETED=""
COMPLETED_COUNT=0
TRAP_ACTIVE=0
TEMP_MANIFEST=""

mark() {
  COMPLETED_COUNT=$((COMPLETED_COUNT + 1))
  COMPLETED="${COMPLETED}  - $1
"
}

on_exit() {
  status=$?
  if [ -n "$TEMP_MANIFEST" ] && [ -f "$TEMP_MANIFEST" ]; then
    rm -f "$TEMP_MANIFEST"
  fi
  if [ "$TRAP_ACTIVE" -eq 1 ] && [ "$status" -ne 0 ]; then
    printf 'rollback-kikoto: rollback did not complete (exit %s).\n' "$status" >&2
    if [ "$COMPLETED_COUNT" -eq 0 ]; then
      printf 'Completed steps: none\n' >&2
    else
      printf 'Completed steps:\n%s' "$COMPLETED" >&2
    fi
  fi
}
trap on_exit EXIT

fail() {
  printf 'rollback-kikoto: %s\n' "$*" >&2
  exit 1
}

compose() {
  if docker compose version >/dev/null 2>&1; then
    if [ -f "$ENV_FILE" ]; then
      docker compose --project-directory "$DEPLOY_DIR" --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
    else
      docker compose --project-directory "$DEPLOY_DIR" -f "$COMPOSE_FILE" "$@"
    fi
  elif command -v docker-compose >/dev/null 2>&1; then
    if [ -f "$ENV_FILE" ]; then
      docker-compose --project-directory "$DEPLOY_DIR" --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
    else
      docker-compose --project-directory "$DEPLOY_DIR" -f "$COMPOSE_FILE" "$@"
    fi
  else
    fail "docker compose is not available"
  fi
}

TRAP_ACTIVE=1

printf '==> Validating backup and prerequisites\n'
command -v docker >/dev/null 2>&1 || fail "docker is not available"
command -v curl >/dev/null 2>&1 || fail "curl is not available"
command -v sqlite3 >/dev/null 2>&1 || fail "sqlite3 is not available on the host"
[ -d "$BACKUP_DIR" ] || fail "backup directory does not exist: $BACKUP_DIR"
if [ -f "$DEPLOY_DIR" ]; then
  fail "deployment directory is a file: $DEPLOY_DIR"
fi
[ -d "$DEPLOY_DIR" ] || fail "deployment directory does not exist: $DEPLOY_DIR"
[ -f "$COMPOSE_FILE" ] || fail "compose file does not exist: $COMPOSE_FILE"

for file in kikoto.db .env docker-compose.yml SHA256SUMS; do
  [ -f "$BACKUP_DIR/$file" ] || fail "backup is missing $file: $BACKUP_DIR"
done

if ! awk '
  {
    name = $2
    sub(/^\*/, "", name)
    sub(/\r$/, "", name)
    if (name ~ /^\// || name ~ /(^|\/)\.\.(\/|$)/) bad = 1
  }
  END { exit bad ? 1 : 0 }
' "$BACKUP_DIR/SHA256SUMS"; then
  fail "SHA256SUMS contains an absolute or parent-traversing path"
fi

for file in kikoto.db .env docker-compose.yml; do
  if ! awk -v wanted="$file" '
    {
      name = $2
      sub(/^\*/, "", name)
      sub(/\r$/, "", name)
      sub(/^\.\//, "", name)
      if (name == wanted) found = 1
    }
    END { exit found ? 0 : 1 }
  ' "$BACKUP_DIR/SHA256SUMS"; then
    fail "SHA256SUMS does not cover $file"
  fi
done

TEMP_MANIFEST=$(mktemp "${TMPDIR:-/tmp}/kikoto-rollback-sums.XXXXXX")
tr -d '\r' < "$BACKUP_DIR/SHA256SUMS" > "$TEMP_MANIFEST"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$BACKUP_DIR" && sha256sum -c "$TEMP_MANIFEST" >/dev/null) || fail "SHA256 verification failed"
elif command -v shasum >/dev/null 2>&1; then
  (cd "$BACKUP_DIR" && shasum -a 256 -c "$TEMP_MANIFEST" >/dev/null) || fail "SHA256 verification failed"
else
  fail "sha256sum or shasum is required"
fi

docker image inspect "$IMAGE" >/dev/null 2>&1 || fail "image is not available locally: $IMAGE"
mark "validated backup, manifest, image and host tools"

STAMP=$(date -u +%Y%m%dT%H%M%SZ)-$$
SAFETY_DIR="$DEPLOY_DIR/rollback-previous-$STAMP"

printf '==> Stopping the Compose service\n'
if [ -n "${KIKOTO_COMPOSE_SERVICE:-}" ]; then
  compose stop "$KIKOTO_COMPOSE_SERVICE"
else
  compose stop
fi
mark "stopped the Compose service"

printf '==> Restoring database, environment and Compose file\n'
mkdir -p "$SAFETY_DIR"
if [ -f "$DB_PATH" ]; then
  cp -p "$DB_PATH" "$SAFETY_DIR/kikoto.db"
fi
if [ -f "$ENV_FILE" ]; then
  cp -p "$ENV_FILE" "$SAFETY_DIR/.env"
fi
if [ -f "$COMPOSE_FILE" ]; then
  cp -p "$COMPOSE_FILE" "$SAFETY_DIR/docker-compose.yml"
fi

mkdir -p "$(dirname "$DB_PATH")"
mkdir -p "$(dirname "$ENV_FILE")"
mkdir -p "$(dirname "$COMPOSE_FILE")"

if [ -f "$DB_PATH" ]; then
  mv "$DB_PATH" "$DB_PATH.broken.$STAMP"
fi
rm -f "$DB_PATH-wal" "$DB_PATH-shm"
cp "$BACKUP_DIR/kikoto.db" "$DB_PATH"
chmod 600 "$DB_PATH"
cp "$BACKUP_DIR/.env" "$ENV_FILE"
chmod 600 "$ENV_FILE"
cp "$BACKUP_DIR/docker-compose.yml" "$COMPOSE_FILE"
mark "restored files and kept a safety copy at $SAFETY_DIR"

printf '==> Starting image %s\n' "$IMAGE"
KIKOTO_IMAGE="$IMAGE"
export KIKOTO_IMAGE
if [ -n "${KIKOTO_COMPOSE_SERVICE:-}" ]; then
  compose up -d --no-build --force-recreate "$KIKOTO_COMPOSE_SERVICE"
else
  compose up -d --no-build --force-recreate
fi
mark "started image $IMAGE"

printf '==> Waiting for %s (timeout %ss)\n' "$HEALTH_URL" "$TIMEOUT"
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$HEALTH_URL" 2>/dev/null || true)
  if [ "$code" = "200" ]; then
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    fail "health check did not return 200 before the timeout: $HEALTH_URL"
  fi
  sleep 2
done
mark "health check returned 200"

printf '==> Verifying SQLite schema and integrity\n'
if ! version=$(sqlite3 "$DB_PATH" "SELECT current_version FROM schema_state WHERE id=1;" 2>&1); then
  fail "could not read schema_state: $version"
fi
[ "$version" = "$EXPECTED_SCHEMA" ] || fail "schema version is $version, expected $EXPECTED_SCHEMA"

if ! quick=$(sqlite3 "$DB_PATH" "PRAGMA quick_check;" 2>&1); then
  fail "PRAGMA quick_check failed: $quick"
fi
[ "$quick" = "ok" ] || fail "PRAGMA quick_check returned: $quick"

if ! foreign_keys=$(sqlite3 "$DB_PATH" "PRAGMA foreign_key_check;" 2>&1); then
  fail "PRAGMA foreign_key_check failed: $foreign_keys"
fi
[ -z "$foreign_keys" ] || fail "PRAGMA foreign_key_check reported rows: $foreign_keys"
mark "verified schema $EXPECTED_SCHEMA, quick_check and foreign_key_check"

printf 'Rollback complete: schema %s, image %s.\n' "$EXPECTED_SCHEMA" "$IMAGE"
