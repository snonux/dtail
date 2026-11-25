# Test Execution Details

Agent notes, indexed in [AGENTS.md](../../AGENTS.md). A section named in
quotes (see "Wire Framing") is another file of this directory; the index
maps the names to files. Update this file with the code it describes.

- Integration tests require binaries to be built before execution
- **IMPORTANT:** Always recompile binaries after code changes before running integration tests:
  ```bash
  make clean && make build
  DTAIL_INTEGRATION_TEST_RUN_MODE=yes make test
  ```
- Integration tests are run by setting DTAIL_INTEGRATION_TEST_RUN_MODE to yes, and by running 'make test'
- `DTAIL_INTEGRATION_TEST_RUN_MODE=yes` is a compatibility shortcut for the
  unchanged test harness. During config initialization it supplies
  `HostnameOverride=integrationtest`, `KnownHostsPath=./known_hosts`,
  `AuthorizedKeysPath=<DTAIL_AUTH_KEY_PATH>.pub` (or `./id_rsa.pub` when that
  key variable is unset), and
  `HostKeyPath=./ssh_host_key` only when those values were not set explicitly.
  Runtime handlers and SSH packages consume these fields and do not inspect the
  integration-mode environment variable themselves.
- Integration tests verify: DCat, DGrep, DMap (MapReduce), DServer, DTail, DTailHealth, and auth-key fast reconnect functionality
- All tests run with race detection enabled (`--race` flag)
- Interactive integration tests (`integrationtests/interactive_*_test.go`,
  `interactive_e2e_test.go` for query completion through to the result
  table, control colors on/off and permission-filtered path listings, the
  latter serverless with `Server.Permissions` from the client's `--cfg`) drive
  `dtail`/`dmap` on a PTY with `runInteractivePTYCommand`
  (`integrationtests/interactive_pty_harness_test.go`, python3 `pty`;
  `interactiveStep.Raw` sends keys without Enter, `Delay` sleeps before the
  input). A `WaitFor`/`WaitForFile` is an assertion: if it does not appear
  within 30 s, or the process exits first, the harness kills the process and
  the run fails naming the step (`ptyStepFailure`); the end of the test
  context kills the process too. A `WaitFor` matches anywhere in the output
  so far, so wait for text only the step produces (e.g. `invert`, not
  `servers=`, which client STATS lines carry too; a data line rather than a
  file name a listing or log line may also carry), and keep edited lines
  under the PTY's 80 columns: a wrapped line is not drawn as one piece.
  `interactive_default_test.go` checks that `dtail` is interactive on a
  terminal without `--interactive-query` and not with `--no-interactive`;
  `pty_harness_test.go` tests the harness itself (no binaries needed).
