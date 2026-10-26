#!/bin/sh
# Fail when a private name leaked into a tracked file.
#
# The name list is NOT part of this repository: copy .private-names.example to
# .private-names (git-ignored), one extended regular expression per line, and
# run this before publishing or cutting a release (AGENTS.md G031).
#
#   sh scripts/check-private-names.sh
#   PRIVATE_NAMES_FILE=/tmp/my.list sh scripts/check-private-names.sh
#
# Exit: 0 = clean, 1 = hits listed on stderr, 2 = no list / not a git tree.
set -eu

list=${PRIVATE_NAMES_FILE:-.private-names}

root=$(git rev-parse --show-toplevel 2>/dev/null) || {
	printf 'check-private-names: run this inside a git work tree\n' >&2
	exit 2
}
cd "$root"

if [ ! -f "$list" ]; then
	printf 'check-private-names: %s not found\n' "$list" >&2
	printf 'copy .private-names.example to %s and fill in your own names\n' "$list" >&2
	exit 2
fi

# One ERE alternation per non-comment line, trimmed and joined with "|".
pattern=$(awk '
	{ sub(/\r$/, "") }
	/^[[:space:]]*(#|$)/ { next }
	{
		gsub(/^[[:space:]]+/, "")
		gsub(/[[:space:]]+$/, "")
		seen = (seen == "") ? $0 : seen "|" $0
	}
	END { print seen }
' "$list")

if [ -z "$pattern" ]; then
	printf 'check-private-names: %s lists no patterns\n' "$list" >&2
	exit 2
fi

# git grep covers exactly the tracked files (same set as `git ls-files`),
# skips binaries and honours .gitattributes.
set +e
hits=$(git grep -InE "$pattern")
rc=$?
set -e

case $rc in
0)
	printf 'check-private-names: private names found in tracked files:\n\n%s\n\n' "$hits" >&2
	printf 'replace them with neutral examples (acme-app, w-office, office-pc, 192.0.2.x, ...)\n' >&2
	exit 1
	;;
1)
	printf 'check-private-names: clean (%s tracked files against %s)\n' \
		"$(git ls-files | wc -l | tr -d ' ')" "$list"
	exit 0
	;;
*)
	printf 'check-private-names: git grep failed (exit %d)\n' "$rc" >&2
	exit 2
	;;
esac
