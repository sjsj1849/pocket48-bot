#!/usr/bin/env bash
# After fetch_real_streets.py completes, run this to:
# 1. Convert JSON → Go source
# 2. Remove the empty var declaration from addrgen.go (stub)
# 3. Build the project
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

echo "=== Step 1: JSON → Go source ==="
python3 "$SCRIPT_DIR/json_to_go.py"

echo ""
echo "=== Step 2: Remove stub declaration from addrgen.go ==="
# The stub has: var realAddresses = map[string][]cityRealAddrs{}
# After real_addresses.go is generated, this stub conflicts.
# Remove the 4-line declaration block.
STUB_FILE="$PROJECT_DIR/internal/addrgen/addrgen.go"
sed -i '/^\/\/ realAddresses maps country code/,/^var realAddresses = map\[string\]\[\]cityRealAddrs{}$/d' "$STUB_FILE"
echo "  Removed stub var realAddresses from addrgen.go"

echo ""
echo "=== Step 3: Build ==="
cd "$PROJECT_DIR"
go build -o pocket48-bot ./cmd/bot/ 2>&1
echo "  BUILD OK: pocket48-bot"

echo ""
echo "=== Final check ==="
go vet ./internal/addrgen/ 2>&1
echo "  VET OK"
