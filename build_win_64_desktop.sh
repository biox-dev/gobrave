#!/bin/bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags "gtk3 embed_frontend" \
  -o gobrave-windows-amd64-desktop.exe ./cmd/desktop
