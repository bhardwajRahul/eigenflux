#!/usr/bin/env bash
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
source "$repo/scripts/cloud/deploy_main_lib.sh"
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT

# All external operations are fixtures; no services or network are contacted.
systemctl() { printf '%s\n' "$*" >> "$case_root/services"; }
go() {
  [[ "$scenario" != build-failure ]] || return 1
  printf 'binary\n' > "$3"
}
curl() {
  local count=0
  [[ ! -f "$case_root/curls" ]] || count=$(< "$case_root/curls")
  printf '%s' "$((count + 1))" > "$case_root/curls"
  [[ "$scenario" != health-failure || "$count" == 0 ]]
}
# Production is GNU/Linux; normalize atomic symlink rename on macOS fixtures.
mv() {
  if [[ "${1:-}" == -Tf && "$(uname)" == Darwin ]]; then
    shift; command mv -fh "$@"
  else
    command mv "$@"
  fi
}

deploy_main_api_matches() { [[ "$scenario" != binary-mismatch ]]; }

# An instance drop-in starting with a digit loses to template deployer.conf.
[[ zz-api-only.conf > deployer.conf ]]
grep -Fq 'override_file=${override_dir}/zz-api-only.conf' "$repo/scripts/cloud/deploy_main_lib.sh"

for scenario in success build-failure health-failure binary-mismatch; do
  case_root="$test_root/$scenario"
  mkdir -p "$case_root/source/scripts/cloud" "$case_root/state" "$case_root/override" "$case_root/old"
  printf '#!/bin/bash\n[[ "$1" == api ]]\n' > "$case_root/source/scripts/cloud/restart.sh"
  printf 'old-source\n' > "$case_root/old/resource"
  ln -s "$case_root/old" "$case_root/state/current"
  ln -s "$case_root/old" "$case_root/state/api-current"
  printf 'old-override\n' > "$case_root/override/zz-api-only.conf"
  if deploy_main_api_only "$case_root/source" "$case_root/state" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "$case_root/override"; then
    [[ "$scenario" == success ]]
    [[ "$(readlink "$case_root/state/api-current")" != "$case_root/old" ]]
    [[ -f "$case_root/state/api-deployed-sha" ]]
  else
    [[ "$scenario" != success ]]
    [[ "$(readlink "$case_root/state/api-current")" == "$case_root/old" ]]
    [[ "$(< "$case_root/override/zz-api-only.conf")" == old-override ]]
    [[ ! -f "$case_root/state/api-deployed-sha" ]]
  fi
  [[ "$(readlink "$case_root/state/current")" == "$case_root/old" ]]
  [[ "$(< "$case_root/old/resource")" == old-source ]]
  if [[ -f "$case_root/services" ]]; then
    ! grep -E 'etcd|feed|profile|auth|pipeline|cron' "$case_root/services"
  fi
  echo "PASS: $scenario preserves other services and restores API on failure"
done

# API mode exits before the full build/migration path; mode rejects extra args.
if deploy_main_run /missing /missing '' '' /missing /missing /missing --api-only extra; then
  echo 'FAIL: extra API deployment arguments accepted' >&2
  exit 1
fi
echo 'PASS: API mode rejects unexpected arguments'
