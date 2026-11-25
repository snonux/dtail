# Developing DTail

For work on the DTail code base itself. The repository's `AGENTS.md` is a
short index (commands, layout); the detailed developer record it links to is
in `docs/agents/*.md`. Read the sections that cover the code before changing
it.

## Layout

- `cmd/<binary>/main.go`: `dcat`, `dgrep`, `dtail`, `dmap`, `dtailhealth`,
  `dserver`, `dtail-tools`. Shared CLI logic lives in `internal/cli`.
- `internal/clients` (clients, interactive mode), `internal/server`,
  `internal/handlers` (session handlers, read engine), `internal/mapr`
  (query language, aggregation, `logformat`), `internal/jobs`,
  `internal/config`, `internal/ssh`, `internal/io`, `internal/protocol`,
  `internal/sessionuser` (permissions), `internal/authkey`.
- User docs in `doc/`, developer docs in `docs/dev/` and `docs/agents/`,
  examples in
  `examples/`, this skill in `docs/skill/dtail/`.

## Build

```sh
make build            # all binaries into the repo root (also: make all)
make install          # go install the six product binaries (not dtail-tools) into $GOBIN
make clean            # also deletes every *.tmp and *.prof file
make dserver          # or dcat, dgrep, dmap, dtail, dtailhealth, dtail-tools
DTAIL_USE_ACL=yes make build   # tag linuxacl, needs libacl-devel
DTAIL_NO_ZSTD=yes make build   # tag nozstd
DTAIL_USE_PROPRIETARY=yes make build   # tag proprietary; fails without the non-public sources
make vet; make lint; make todos        # lint needs golangci-lint
```

## Test

```sh
make test                                          # unit tests: -race, shuffled, 30 min timeout
make clean && make build && DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test
make test-integration                              # clean, build, integration tests
cd integrationtests && DTAIL_INTEGRATION_TEST_RUN_MODE=yes go test -run TestName
```

- Integration tests run the built binaries: always rebuild first. Without
  `DTAIL_INTEGRATION_TEST_RUN_MODE=yes` they are skipped.
- `DTAIL_INTEGRATION_TEST_RUN_MODE=yes` makes config initialization use
  hostname `integrationtest`, `./known_hosts`, `./id_rsa.pub` (or
  `$DTAIL_AUTH_KEY_PATH.pub`) and `./ssh_host_key` unless set explicitly.
- Interactive tests drive `dtail`/`dmap` on a PTY via python3 `pty`
  (`integrationtests/interactive_pty_harness_test.go`); `WaitFor` steps
  fail after 30 s; keep edited lines under 80 columns.
- Doc-coupled tests: `TestVocabularyMatchesDoc` (query vocabulary vs
  `doc/querylanguage.md`), `TestInteractiveHelpExamplesParse`, the config
  schema test (`examples/dtail.schema.json`).
- Docker server farm for manual tests: `doc/testing.md`.
- Documentation GIFs: `make gifs` renders `doc/*.gif` from the VHS tapes in
  `doc/tapes/` (shared look in `_settings.tape`) against a demo fleet of six
  local dservers that `doc/tapes/render.sh` starts; re-render after
  user-visible output changes.

## Benchmarks, profiles, PGO

```sh
make benchmark | benchmark-quick | benchmark-full
make benchmark-baseline; make benchmark-compare BASELINE=benchmarks/baselines/baseline_TIMESTAMP.txt
make benchmark-upstream-smoke     # vs ../dtail-upstream checkout, no timings
make benchmark-upstream           # paired timings; idle host only
make drop-caches                  # needs sudo
make profile-all | profile-quick | profile-dmap | profile-list | profile-clean
make profile-analyze PROFILE=profiles/dcat_cpu_*.prof     # or profile-web
make pgo | pgo-quick | pgo-generate | build-pgo | install-pgo | pgo-clean
make pgo-commands COMMANDS='dcat dgrep'
dtail-tools benchmark -mode run|baseline|compare|list|clean
dtail-tools profile -mode quick|full|dmap|analyze|list
dtail-tools pgo [-profiledir -outdir -datasize -iterations -v -profileonly] [cmds]
```

- Profiles go to `profiles/`; PGO profiles to `pgo-profiles/` and binaries
  to `pgo-build/` (both gitignored, regenerate after hot-path changes).
- PGO gains are modest and workload-dependent (dgrep ~7-8%, dcat ~3%);
  output is byte-identical. Known issue: the final timing comparison of
  `dtail-tools pgo` knows only dcat, dgrep and dmap, so a run with the
  default command set ends with `unknown benchmark command` after the
  profiles and PGO binaries were written.
- `make benchmark-baseline*` prompt for a name (interactive).
- The data clients take `--cpuprofile`, `--memprofile`, `--profile`,
  `--profiledir`; the clients, `dtailhealth` and `dserver` take
  `--pprof ADDR` (`dtail-tools` does not).

## Conventions

- Add query vocabulary to its table (`keywords`, `aggregationTable`,
  `whereOperatorTable`, `funcs.functionTable`) and update
  `doc/querylanguage.md`.
- New config fields need the schema and example updated.
- Clients and dserver share protocol 5; wire changes need both sides.
- Keep the docs, `AGENTS.md`, `docs/agents/` and this skill in sync with
  user-visible changes.
