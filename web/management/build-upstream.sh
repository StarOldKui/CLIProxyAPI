#!/usr/bin/env bash

set -euo pipefail

if [[ "${1:-}" != "" ]]; then
  echo "Usage: ./web/management/build-upstream.sh"
  exit 1
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
upstream_url="https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git"
upstream_commit="6586f88858ca27e840bd8db2630dccd371a1cd4a"
patch_path="${repo_root}/web/management/upstream-patches/management-center-6586f888-quota-autorefresh.patch"
output_path="${repo_root}/internal/managementasset/static/management.html"
build_dir="$(mktemp -d "${TMPDIR:-/tmp}/cli-proxy-management.XXXXXX")"
trap 'rm -rf "${build_dir}"' EXIT

# Pinning the upstream commit keeps the deployed panel reproducible while preserving local quota behavior as a reviewable patch.
git -C "${build_dir}" init --quiet
git -C "${build_dir}" remote add origin "${upstream_url}"
git -C "${build_dir}" fetch --quiet --depth 1 origin "${upstream_commit}"
git -C "${build_dir}" checkout --quiet --detach FETCH_HEAD
git -C "${build_dir}" apply --unidiff-zero --check "${patch_path}"
git -C "${build_dir}" apply --unidiff-zero "${patch_path}"

cd "${build_dir}"
npx --yes bun@1.3.14 install --frozen-lockfile
npx --yes bun@1.3.14 run build
install -m 0644 dist/index.html "${output_path}.tmp"
mv "${output_path}.tmp" "${output_path}"

echo "Built ${output_path} from ${upstream_commit}."
