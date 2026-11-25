#!/usr/bin/env bash
# Renders the GIFs of the documentation from the VHS tapes in this directory
# (https://github.com/charmbracelet/vhs) into doc/.
#
# Usage (from the repository root, after `make build`):
#   doc/tapes/render.sh [TAPE ...]     default: every *.tape not starting with _
#
# The recordings run against a small demo fleet on this machine: six dserver
# processes on 127.0.0.1 (ports 23231-23236) named web01, web02, web03,
# api01, api02 and db01, each in a directory of its own with its own seeded
# logs (demo-logs.sh), and a generator per server appending a line to its
# logs/app.log about every 0.3 s. The client side runs with a demo HOME: its
# own SSH key, a known_hosts file trusting the six servers, a DTail config
# (hostname "demo", log level warn), a neutral `$ ` prompt and servers.txt.
# So the GIFs show neither the real host name, user, paths nor SSH agent.
#
# Every tape starts with `Source _settings.tape` (one theme, font, size and
# typing speed for all GIFs) and runs in the demo HOME, or in the repository
# root when it has the line `# render-cwd: repo`. The repository root comes
# first on PATH, so the freshly built binaries are the ones recorded.
set -euo pipefail

TAPE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$TAPE_DIR/../.." && pwd)"
declare -r TAPE_DIR REPO_DIR
declare -r OUT_DIR="$REPO_DIR/doc"
declare -ra SERVERS=(web01 web02 web03 api01 api02 db01)
declare -ri BASE_PORT=23231

declare ROOT=''
declare -a PIDS=()
GO_PATH="$(go env GOPATH)"
GO_MODCACHE="$(go env GOMODCACHE)"
GO_CACHE="$(go env GOCACHE)"
declare -r GO_PATH GO_MODCACHE GO_CACHE

_cleanup() {
    if ((${#PIDS[@]} > 0)); then
        kill "${PIDS[@]}" 2>/dev/null || true
        wait "${PIDS[@]}" 2>/dev/null || true
    fi
    if [[ -n "$ROOT" ]]; then
        chmod -R u+w "$ROOT" 2>/dev/null || true
        rm -rf "$ROOT"
    fi
}

_require() {
    local cmd
    for cmd in vhs gifsicle ssh-keygen go; do
        if ! command -v "$cmd" >/dev/null; then
            echo "render.sh: $cmd is required" >&2
            exit 1
        fi
    done
    for cmd in dtail dmap dcat dgrep dserver dtailhealth; do
        if [[ ! -x "$REPO_DIR/$cmd" ]]; then
            echo "render.sh: $REPO_DIR/$cmd missing, run make build" >&2
            exit 1
        fi
    done
}

# Runs a command with the demo client environment. TZ=UTC keeps the local
# time zone out of timestamps and $timezone; the Go caches stay the real ones,
# so a tape running go test downloads and rebuilds nothing into the demo HOME.
_client_env() {
    env -u SSH_AUTH_SOCK TZ=UTC HOME="$ROOT/home" PATH="$REPO_DIR:$PATH" \
        GOPATH="$GO_PATH" GOMODCACHE="$GO_MODCACHE" GOCACHE="$GO_CACHE" "$@"
}

_setup_home() {
    local -r home="$ROOT/home"
    local -i i
    mkdir -p "$home/.ssh" "$home/.config/dtail" "$home/logs"
    ssh-keygen -q -t ed25519 -N '' -C demo -f "$home/.ssh/id_ed25519"
    cat >"$home/.config/dtail/dtail.conf" <<'EOF'
{
  "Common": {
    "LogLevel": "warn",
    "HostnameOverride": "demo"
  },
  "Client": {
    "AuthKeyDisable": true
  }
}
EOF
    printf '%s\n' "PS1='\$ '" "export HISTFILE=/dev/null" >"$home/.bashrc"
    for ((i = 0; i < ${#SERVERS[@]}; i++)); do
        printf 'localhost:%d\n' $((BASE_PORT + i))
    done >"$home/servers.txt"
    # Local files for the serverless tape.
    "$TAPE_DIR/demo-logs.sh" init "$home" 11 0
    cat >"$home/users.csv" <<'EOF'
name,team,age,logins
alice,ops,34,120
bob,dev,45,80
carol,dev,29,230
dave,ops,52,40
erin,sec,41,95
frank,dev,38,150
EOF
    cp "$TAPE_DIR/_settings.tape" "$home/_settings.tape"
}

_start_server() {
    local -r name="$1"
    local -ri index="$2"
    local -ri port=$((BASE_PORT + index))
    local -r dir="$ROOT/servers/$name"
    local -i slow=0
    if [[ "$name" == web02 ]]; then
        slow=60
    fi
    "$TAPE_DIR/demo-logs.sh" init "$dir" $((index + 1)) "$slow"
    cat >"$dir/dserver.json" <<EOF
{
  "Common": {
    "SSHPort": $port,
    "Logger": "file",
    "LogDir": "log",
    "CacheDir": "cache"
  },
  "Server": {
    "SSHBindAddress": "127.0.0.1",
    "HostKeyBits": 2048,
    "AuthorizedKeysPath": "$ROOT/home/.ssh/id_ed25519.pub",
    "MaxConnections": 50,
    "MaxConcurrentCats": 10,
    "Permissions": { "Default": ["readfiles:^/"] }
  }
}
EOF
    (cd "$dir" && TZ=UTC exec "$REPO_DIR/dserver" -cfg dserver.json \
        -hostname-override "$name") >/dev/null 2>&1 &
    PIDS+=($!)
    TZ=UTC "$TAPE_DIR/demo-logs.sh" follow "$dir" $((index + 1)) "$slow" &
    PIDS+=($!)
}

_wait_for_port() {
    local -ri port="$1"
    local -i tries
    for ((tries = 0; tries < 100; tries++)); do
        if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
            return
        fi
        sleep 0.1
    done
    echo "render.sh: dserver on port $port did not start" >&2
    exit 1
}

_setup_fleet() {
    local -i i
    for ((i = 0; i < ${#SERVERS[@]}; i++)); do
        _start_server "${SERVERS[i]}" "$i"
    done
    for ((i = 0; i < ${#SERVERS[@]}; i++)); do
        _wait_for_port $((BASE_PORT + i))
    done
    # Trust the demo servers' host keys once, so no tape is asked about them.
    # (dcat has no --no-interactive; with stdin not a terminal it never
    # becomes interactive anyway.)
    if ! (cd "$ROOT/home" && _client_env dcat --trustAllHosts \
        --servers servers.txt --files version.txt </dev/null >/dev/null 2>&1); then
        echo "render.sh: the demo fleet does not answer" >&2
        exit 1
    fi
}

_render() {
    local -r tape="$1"
    local -r name="$(basename "$tape" .tape)"
    local cwd="$ROOT/home"
    if grep -q '^# render-cwd: repo' "$tape"; then
        cwd="$REPO_DIR"
        cp "$TAPE_DIR/_settings.tape" "$cwd/_settings.tape"
    fi
    echo "rendering $name.gif"
    (cd "$cwd" && _client_env vhs "$tape" >/dev/null)
    gifsicle -O3 --lossy=30 "$cwd/$name.gif" -o "$OUT_DIR/$name.gif"
    rm -f "$cwd/$name.gif"
    if [[ "$cwd" == "$REPO_DIR" ]]; then
        rm -f "$cwd/_settings.tape"
    fi
}

main() {
    local -a tapes=("$@")
    local tape
    _require
    if ((${#tapes[@]} == 0)); then
        for tape in "$TAPE_DIR"/[!_]*.tape; do
            tapes+=("$tape")
        done
    fi
    trap _cleanup EXIT
    ROOT="$(mktemp -d)"
    _setup_home
    _setup_fleet
    for tape in "${tapes[@]}"; do
        _render "$(realpath "$tape")"
    done
}

main "$@"
