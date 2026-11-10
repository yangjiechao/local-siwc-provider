#!/bin/bash
# macOS: double-click this file to build (when needed) and open the local dashboard.
set -e
cd "$(dirname "$0")"
needs_build=false
if [[ ! -x ./siwc-provider ]]; then
  needs_build=true
elif command -v go >/dev/null 2>&1 && [[ -d .git ]]; then
  # Rebuild with its exact source commit to make local/remote comparison accurate.
  needs_build=true
fi
if [[ "$needs_build" == true ]]; then
  if ! command -v go >/dev/null 2>&1; then
    printf '\nGo 1.23+ is required for the first build. Install Go from https://go.dev/dl/ and reopen this file.\n'
    read -r -p 'Press Return to close... ' _
    exit 1
  fi
  echo "Building Local SIWC Provider..."
  commit=""
  if command -v git >/dev/null 2>&1 && [[ -d .git ]]; then
    commit="$(git rev-parse HEAD)"
  fi
  if [[ -n "$commit" ]]; then
    go build -ldflags="-X main.buildCommit=${commit}" -o siwc-provider .
  else
    go build -o siwc-provider .
  fi
fi
printf '\nLocal SIWC Provider is starting...\nKeep this Terminal window open while you use the dashboard.\n'
./siwc-provider ui --dashboard-port 8787
status=$?
if [[ $status -ne 0 ]]; then
  printf '\nStart failed. Check whether management port 8787 is already in use.\n'
  read -r -p 'Press Return to close... ' _
fi
exit "$status"
