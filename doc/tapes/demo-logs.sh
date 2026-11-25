#!/usr/bin/env bash
# Writes the demo log files that the VHS tapes in this directory record
# against. The lines use DTail's default log format, so the MapReduce
# examples can query them with `from REQUESTS` and `from STATS`:
#
#   INFO|TIME|PID|CALLER|CPUS|GOROUTINES|CGOCALLS|LOADAVG|UPTIME|MAPREDUCE:TABLE|k=v|...
#
# Usage:
#   demo-logs.sh init DIR [SEED [SLOW_MS]]
#       create DIR/logs/{app,db}.log with history and DIR/version.txt
#   demo-logs.sh follow DIR [SEED [SLOW_MS]]
#       append a line to DIR/logs/app.log about every 0.3 s until killed
#       (for the dtail tapes)
#
# SEED varies the content per demo server; SLOW_MS adds latency to its
# requests, so the per-host tables differ. The content is pseudo-random but
# seeded, so re-rendering a tape shows the same history. No real host names
# or paths end up in the recordings.
set -euo pipefail

declare -ra SERVICES=(web api auth search billing)
declare -ra METHODS=(GET GET GET POST PUT)
declare -ra STATUSES=(200 200 200 200 201 304 404 500 503)

# Prints one REQUESTS line, or now and then a STATS or WARN line.
_line() {
    local -r stamp="$1"
    local -ri slow="$2"
    local -i pick=$((RANDOM % 20))
    local -r head="${stamp}|4242|server.go:88|8|$((30 + RANDOM % 20))|7|0.35|12h3m"

    if ((pick == 0)); then
        printf 'WARN|%s|timeout talking to db after %dms\n' \
            "$stamp" $((900 + RANDOM % 300))
        return
    fi
    if ((pick == 1)); then
        printf 'INFO|%s|MAPREDUCE:STATS|currentConnections=%d|lifetimeConnections=%d\n' \
            "$head" $((RANDOM % 12)) $((1000 + RANDOM % 50))
        return
    fi
    local -r service="${SERVICES[RANDOM % ${#SERVICES[@]}]}"
    local -r method="${METHODS[RANDOM % ${#METHODS[@]}]}"
    local -r status="${STATUSES[RANDOM % ${#STATUSES[@]}]}"
    local -i latency=$((5 + slow + RANDOM % 80))
    if [[ "$service" == search ]]; then
        latency+=120
    fi
    printf 'INFO|%s|MAPREDUCE:REQUESTS|service=%s|method=%s|status=%s|latency=%d\n' \
        "$head" "$service" "$method" "$status" "$latency"
}

_init() {
    local -r dir="$1"
    local -ri seed="$2" slow="$3"
    local -i i
    RANDOM=$seed
    mkdir -p "$dir/logs"
    for ((i = 0; i < 2000; i++)); do
        _line "$(printf '20261125-%02d%02d%02d' \
            $((8 + i / 400)) $((i / 7 % 60)) $((i % 60)))" "$slow"
    done >"$dir/logs/app.log"
    for ((i = 0; i < 200; i++)); do
        printf 'INFO|20261125-09%02d%02d|4243|db.go:12|4|20|3|0.10|3h|MAPREDUCE:QUERIES|table=%s|rows=%d\n' \
            $((i / 60 % 60)) $((i % 60)) \
            "${SERVICES[i % ${#SERVICES[@]}]}" $((RANDOM % 500))
    done >"$dir/logs/db.log"
    # A rollout in progress: every third server still runs the old version.
    if ((seed % 3 == 0)); then
        echo 'app version 1.4.9 (build 2026-10-30)'
    else
        echo 'app version 1.5.0 (build 2026-11-20)'
    fi >"$dir/version.txt"
}

_follow() {
    local -r dir="$1"
    local -ri seed="$2" slow="$3"
    RANDOM=$((seed + 1000))
    while true; do
        _line "$(date +%Y%m%d-%H%M%S)" "$slow" >>"$dir/logs/app.log"
        sleep 0.3
    done
}

main() {
    local -r mode="${1:-}"
    local -r dir="${2:-}"
    local -r seed="${3:-42}"
    local -r slow="${4:-0}"
    if [[ -z "$dir" ]]; then
        echo "usage: $0 init|follow DIR [SEED [SLOW_MS]]" >&2
        exit 2
    fi
    case "$mode" in
        init) _init "$dir" "$seed" "$slow" ;;
        follow) _follow "$dir" "$seed" "$slow" ;;
        *)
            echo "usage: $0 init|follow DIR [SEED [SLOW_MS]]" >&2
            exit 2
            ;;
    esac
}

main "$@"
