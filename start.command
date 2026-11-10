#!/bin/bash
# Double-click on macOS to build when needed and open the local dashboard.
set -e
cd "$(dirname "$0")"
if [[ ! -x ./siwc-provider || main.go -nt ./siwc-provider || dashboard.go -nt ./siwc-provider || dashboard/app.js -nt ./siwc-provider ]]; then
  if ! command -v go >/dev/null 2>&1; then
    printf '\nGo 1.23+ is required. Install Go from https://go.dev/dl/ and reopen this file.\n'
    read -r -p 'Press Return to close... ' _
    exit 1
  fi
  echo "Building Local SIWC Provider..."
  go build -o siwc-provider .
fi
printf '\nLocal SIWC Provider is starting...\nKeep this Terminal window open while using the dashboard.\n'
./siwc-provider ui --port 8787
status=$?
if [[ $status -ne 0 ]]; then
  printf '\nStart failed. Check whether port 8787 is already in use.\n'
  read -r -p 'Press Return to close... ' _
fi
exit "$status"
