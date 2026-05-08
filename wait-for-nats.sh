#!/bin/sh
# wait-for-nats.sh — blocks until NATS is accepting connections
# Usage: ./wait-for-nats.sh nats:4222 -- command args...

HOST=$(echo "$1" | cut -d: -f1)
PORT=$(echo "$1" | cut -d: -f2)
shift
shift  # skip the "--"

echo "Waiting for NATS at $HOST:$PORT..."
until nc -z "$HOST" "$PORT" 2>/dev/null; do
  sleep 1
done
echo "NATS is up."
exec "$@"
