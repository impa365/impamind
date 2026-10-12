#!/bin/sh
exec wget -S -O- \
  --header="Authorization: Bearer $TOKEN" \
  --header="Content-Type: application/json" \
  --header="Accept: application/json, text/event-stream" \
  --header="Host: memoria.impa365.cloud" \
  --post-data='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"m1","version":"0"}}}' \
  "$1"
