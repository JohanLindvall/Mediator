#!/bin/sh
# Lists what in the tracked files could be a real name — a file, a release,
# a performer, a site, a personal path — for a reader to judge. It cannot
# know a name when it sees one, so it looks for the shapes names take, and
# a hit is a question, not a verdict: an invented fixture has the shape too,
# which is the whole point of a fixture. See CLAUDE.md, "Writing about this
# project". Exit status is 0 whatever it finds; the reading is the check.
#
# Usage: scripts/names.sh            (from the repository root, or `make names`)
set -eu
cd "$(dirname "$0")/.."

files() {
  git ls-files -z | grep -z -v -E '^(web/package-lock\.json|web/src/types\.gen\.ts|go\.sum)$' \
    | grep -z -v -E '\.(png|jpg|jpeg|gif|ico|woff2?|db)$'
}

section() { printf '\n== %s\n' "$1"; }

section "personal paths and mounts (want none; /media and /srv are generic and not listed)"
files | xargs -0 grep -n -E '(^|[^a-zA-Z])/(home|mnt|Users)/[A-Za-z0-9_.-]+' -- 2>/dev/null || true

section "hostnames in prose and fixtures, outside the ones the project depends on (a site tag is a name)"
files | grep -z -E '\.md$|_test\.go$|\.test\.ts$' | xargs -0 grep -n -o -E '\b[a-z0-9-]+(\.[a-z0-9-]+)*\.(com|net|org|to|cc|ru|se|de|io|tv|xxx|nu|fi|no|dk)\b' -- 2>/dev/null \
  | grep -v -E 'github\.com|golang\.org|google\.com|googleapis\.com|gstatic\.com|example\.(com|net|org)|example\b|w3\.org|mozilla\.org|apple\.com|microsoft\.com|ffmpeg\.org|etcd\.io|npmjs\.(com|org)|nodejs\.org|jsdelivr\.net|cloudflare\.com|jquery\.com|tailwindcss\.com|docker\.(com|io)|alpinelinux\.org|freedesktop\.org|kernel\.org|whatwg\.org|iana\.org|wikipedia\.org|upnp\.org|xmlsoap\.org|schemas\.|dlna\.org|videolan\.org|matroska\.org|xiph\.org|intel\.com|nvidia\.com|typescriptlang\.org|vitejs\.dev|esbuild\.github|sourcemap|\.min\.js|\.test\.ts' \
  | sort -u || true

section "release-shaped names: dotted words ending in a group tag, or a season marker"
files | xargs -0 grep -n -o -E '\b[A-Za-z0-9]+(\.[A-Za-z0-9]+){2,}-[A-Za-z0-9]{2,}\b|\bS[0-9]{2}E[0-9]{2}\b|\b[1-9][0-9]?x[0-9]{2}\b' -- 2>/dev/null \
  | grep -v -E 'go\.mod|Dockerfile|\.(yml|yaml)|node_modules' | sort -u || true

section "text in another alphabet (a fixture that is not Latin is still a name)"
files | xargs -0 grep -n -P '[\p{Cyrillic}\p{Thai}\p{Hangul}\p{Han}\p{Hiragana}\p{Katakana}\p{Arabic}\p{Hebrew}]' -- 2>/dev/null | cut -c1-140 || true

section "capitalised phrases quoted in tests (read them: each must be invented)"
files | grep -z -E '_test\.go$|\.test\.ts$' | xargs -0 grep -h -o -E '"[A-Z][A-Za-z0-9'"'"'.-]+( [A-Za-z0-9'"'"'&.()-]+){1,6}"' -- 2>/dev/null \
  | sort | uniq -c | sort -rn | awk '{$1=""; print}' | tr '\n' '|' | sed 's/|/ | /g' | fold -s -w 160 || true
echo
