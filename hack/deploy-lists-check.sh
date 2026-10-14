#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Fail when an nvke-deploy.yaml item has no directory; deploy/nvke-deploy.sh dies there.
#
# This check is stricter than the parser on purpose. cli/internal/deploypath
# reads any YAML the schema allows; the committed lists are written in one
# shape, and this check holds them to it, so a list is never parsed loosely
# into fewer items than it claims. The item name set is the parser's
# (letters, digits, `-`, `_`, inner dots) and the type set is the one the
# executor dispatches.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
status=0
lists=0
items=0
# The list is one `items:` line followed by `  - {name: X, type: Y}` lines.
# Any other line that is not blank or a comment is an error.
while IFS= read -r list; do
	lists=$((lists + 1))
	deploy_path="$(dirname "${list}")"
	found=0
	header=0
	while IFS= read -r line || [[ -n "${line}" ]]; do
		[[ "${line}" =~ ^[[:space:]]*(#|$) ]] && continue
		if [[ "${line}" == *$'\r' ]]; then
			echo "ERROR: ${list}: CRLF line ending: ${line%$'\r'}" >&2
			status=1
			continue
		fi
		if [[ "${line}" == "items:" ]]; then
			header=1
			continue
		fi
		if [[ "${header}" -eq 0 ]]; then
			echo "ERROR: ${list}: line before the items: header: ${line}" >&2
			status=1
			continue
		fi
		if [[ "${line}" =~ ^\ \ -\ \{name:\ ([A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*),\ type:\ (manifest|helm|template)\}$ ]]; then
			name="${BASH_REMATCH[1]}"
			type="${BASH_REMATCH[3]}"
			found=1
			items=$((items + 1))
			if [[ ! -d "${deploy_path}/${name}" ]]; then
				echo "ERROR: ${list}: item ${name} (${type}) has no directory at ${deploy_path}/${name}" >&2
				status=1
			fi
			continue
		fi
		echo "ERROR: ${list}: unrecognised line: ${line}" >&2
		status=1
	done <"${list}"
	if [[ ${found} -eq 0 ]]; then
		echo "ERROR: ${list}: no items parsed" >&2
		status=1
	fi
done < <(git ls-files -- '*/nvke-deploy.yaml')

if [[ ${lists} -eq 0 ]]; then
	echo "ERROR: no nvke-deploy.yaml found; the check matched nothing" >&2
	exit 1
fi
[[ ${status} -ne 0 ]] || echo "deploy lists: ${items} item(s) in ${lists} list(s), each with its directory"
exit "${status}"
