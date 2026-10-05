#!/usr/bin/env bash
# Manage the disposable development databases (scripts/dbs/compose.yml).
#   dbs.sh up [service…]   start (all by default) and wait until ready
#   dbs.sh down            stop and remove containers and volumes
#   dbs.sh ps | logs <svc>
set -euo pipefail
cd "$(dirname "$0")"
root=../..
dev=$root/.dev
mkdir -p "$dev/certs" "$dev/ssh"

certs() {
  [ -f "$dev/certs/server.crt" ] && return
  echo "generating test TLS certificates in .dev/certs"
  (
    cd "$dev/certs"
    openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=lumino-test-ca" -keyout ca.key -out ca.crt 2>/dev/null
    openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" -keyout server.key -out server.csr 2>/dev/null
    printf "subjectAltName=DNS:localhost,IP:127.0.0.1,DNS:postgres,DNS:mysql\n" > san.ext
    openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 3650 -extfile san.ext -out server.crt 2>/dev/null
    openssl req -newkey rsa:2048 -nodes -subj "/CN=lumino" -keyout client.key -out client.csr 2>/dev/null
    openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 3650 -out client.crt 2>/dev/null
    chmod 644 server.key client.key
    rm -f ./*.csr san.ext
  )
}

sshkey() {
  [ -f "$dev/ssh/id_ed25519" ] && return
  ssh-keygen -q -t ed25519 -N "" -C lumino-test -f "$dev/ssh/id_ed25519"
  ssh-keygen -q -t ed25519 -N "secret" -C lumino-test-enc -f "$dev/ssh/id_ed25519_enc"
  chmod 644 "$dev/ssh/"*.pub
}

case "${1:-up}" in
  up)
    shift || true
    certs
    sshkey
    docker compose up -d --wait --wait-timeout 600 "$@" 2>&1 | grep -v "Waiting" || true
    docker compose ps --format 'table {{.Service}}\t{{.Status}}'
    ;;
  down)
    docker compose down -v
    ;;
  ps)
    docker compose ps
    ;;
  logs)
    shift
    docker compose logs --tail 100 "$@"
    ;;
  *)
    echo "usage: $0 up [svc…] | down | ps | logs <svc>" >&2
    exit 2
    ;;
esac
