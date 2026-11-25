DTail Testing Guide
===================

DTail is tested in three ways:

1. Unit tests (automatic)
2. Integration tests (automatic)
3. Semi-manual tests with DTail server instances running in Docker

All commands run in the top level source directory.

## Quality control

Lint and vet the code before a release. `make lint` needs `golangci-lint`
(another binary: `GOLANGCI_LINT=... make lint`).

```shell
% make lint
% make vet
```

## Unit tests

```shell
% make test
```

This clears the Go test cache (`go clean -testcache`) and runs `go test -race
-shuffle=on -timeout 30m ./...`: race detector, randomized test order, every
package. A failing package does not stop the others; the target fails at the
end. The integration tests skip themselves in this run.

## Integration tests

The integration tests run the compiled DTail binaries the way a user would, so
the binaries must be built from the current code. The dedicated target cleans,
rebuilds and runs only the integration tests:

```shell
% make test-integration
```

To run them together with the unit tests, build first and set
`DTAIL_INTEGRATION_TEST_RUN_MODE`:

```shell
% make clean && make build
% DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test
```

To run them without a rebuild, or only some of them:

```shell
% DTAIL_INTEGRATION_TEST_RUN_MODE=yes go test -v --race -count=1 ./integrationtests
% DTAIL_INTEGRATION_TEST_RUN_MODE=yes go test --race -count=1 -run 'TestDCat' ./integrationtests
```

Notes:

* **Rebuild after every code change.** Stale binaries test the old code.
* The interactive tests (`integrationtests/interactive_*_test.go` and the
  `dmap` query builder tests) drive `dtail` and `dmap` on a pseudo terminal
  and need `python3` on `PATH`; without it they fail.
* The test servers listen on local ports numbered from 4242 up. To run the
  suite twice on one machine at the same time (e.g. from two git worktrees),
  give one run another base: `DTAIL_INTEGRATION_TEST_PORT_BASE=5241`.

![testing](testing.gif "Integration tests")

## Semi-manual tests with DTail server instances running in Docker

### Requirements

* Docker is up and running.
* All DTail binaries are compiled (`make build`).
* The base image of `docker/Dockerfile` (currently `fedora:42`) must be
  compatible with the glibc your local `dserver` binary was linked against, as
  that binary is copied into the image.

### Building DTail server Docker image

```shell
% make -C docker
```

This generates the SSH key pair `docker/id_rsa_docker`, copies `dserver` and
`integrationtests/mapr_testdata.log` into the build context and builds the
image `dserver:develop`.

### Starting a DTail server farm

To spin up 10 containers (another number: `NUM_INSTANCES=3`):

```shell
% make -C docker spinup
% docker ps
CONTAINER ID   IMAGE             COMMAND                  ...   PORTS                    NAMES
3873f7e69620   dserver:develop   "/usr/local/bin/dser…"   ...   0.0.0.0:2232->2222/tcp   dserver-serv9
...
b34e05f33deb   dserver:develop   "/usr/local/bin/dser…"   ...   0.0.0.0:2223->2222/tcp   dserver-serv0
```

The containers `dserver-serv0` to `dserver-serv9` listen on the local ports
2223 to 2232, which `spinup` writes to `docker/serverlist.txt`.

### Connecting to all 10 DTail servers

`docker/Makefile` has targets for the clients (`dtail`, `dgrep`, `dcat`,
`dmap`, ...). For example, to follow the `dserver` log files of all
containers with `dtail`:

```shell
% make -C docker dtail
```

### Stopping all Docker containers again

```shell
% make -C docker spindown
```
