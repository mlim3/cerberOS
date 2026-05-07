#!/bin/bash
# Generate self-signed TLS certs for NATS demo (NFR-DB-006).
# Output: config/certs/server.pem, server-key.pem, ca.pem
set -e
cd "$(dirname "$0")/.."
CERTS_DIR="config/certs"
mkdir -p "$CERTS_DIR"

# CA
openssl genrsa -out "$CERTS_DIR/ca-key.pem" 2048 2>/dev/null
openssl req -new -x509 -days 365 -key "$CERTS_DIR/ca-key.pem" -out "$CERTS_DIR/ca.pem" \
  -subj "/CN=Aegis-NATS-CA" 2>/dev/null

# Server cert (SAN: nats-1, nats-2, nats-3, localhost for Docker + local clients)
openssl genrsa -out "$CERTS_DIR/server-key.pem" 2048 2>/dev/null
openssl req -new -key "$CERTS_DIR/server-key.pem" -out "$CERTS_DIR/server.csr" \
  -subj "/CN=nats-1" 2>/dev/null
cat > "$CERTS_DIR/ext.cnf" << 'EOF'
subjectAltName = DNS:nats-1,DNS:nats-2,DNS:nats-3,DNS:localhost,IP:127.0.0.1
EOF
openssl x509 -req -in "$CERTS_DIR/server.csr" -CA "$CERTS_DIR/ca.pem" -CAkey "$CERTS_DIR/ca-key.pem" \
  -CAcreateserial -out "$CERTS_DIR/server.pem" -days 365 -extfile "$CERTS_DIR/ext.cnf" 2>/dev/null
rm -f "$CERTS_DIR/server.csr" "$CERTS_DIR/ext.cnf" "$CERTS_DIR/ca.srl"

echo "Generated $CERTS_DIR/server.pem, server-key.pem, ca.pem"
echo "For clients: set AEGIS_NATS_TLS_CA=$CERTS_DIR/ca.pem"
