# Profiling Hang Issue Analysis

## Issue Description
The dtail profiling suite hangs when processing large files in serverless mode. This occurs when running commands like `dcat`, `dgrep`, or `dmap` with `-cfg none` and no servers specified.

## Root Cause
When dtail operates in serverless mode (no servers specified), the `Serverless` connector creates bidirectional `io.Copy` operations between client and server handlers that deadlock on larger files.

### Key Findings
1. **File Size Threshold**: Small files work fine, but files over ~478KB cause hangs
2. **Mode Specific**: The issue only occurs in serverless mode (when no servers are specified)
3. **Deadlock Mechanism**: Two goroutines run `io.Copy` in opposite directions, creating a deadlock when buffers fill up
4. **Profiling Impact**: The profiling example generates a 72MB test file, which triggers this issue

### Code Location
The problematic code is in `/home/paul/git/dtail/internal/clients/connectors/serverless.go:86-98`:

```go
go func() {
    defer terminate()
    if _, err := io.Copy(serverHandler, s.handler); err != nil {
        dlog.Client.Trace(err)
    }
    dlog.Client.Trace("io.Copy(serverHandler, s.handler) => done")
}()
go func() {
    defer terminate()
    if _, err := io.Copy(s.handler, serverHandler); err != nil {
        dlog.Client.Trace(err)
    }
    dlog.Client.Trace("io.Copy(s.handler, serverHandler) => done")
}()
```

## Workaround
Specify a dummy server to avoid serverless mode:
```bash
./dcat -profile -profiledir profiles -plain -cfg none -servers dummy test_data.log
```

## Symptoms
- Command hangs indefinitely when processing large files
- CPU profile files are created but remain at 0 KB
- Multiple profile files are generated as the profiler attempts to write snapshots
- Process must be killed with timeout or Ctrl+C

## Impact
- Profiling benchmarks fail to complete
- Performance analysis of dtail tools is impaired
- Integration tests may hang if they use serverless mode with large files