# Serverless Mode Deadlock Fix Plan

## Problem Summary
The serverless connector uses bidirectional `io.Copy` operations that deadlock when processing large files. This happens because:
1. `io.Copy(serverHandler, s.handler)` reads from client, writes to server
2. `io.Copy(s.handler, serverHandler)` reads from server, writes to client
3. When both buffers fill up, neither can proceed, causing a deadlock

## Proposed Solutions

### Solution 1: Buffered Pipe with Flow Control (Recommended)
Replace direct `io.Copy` with a buffered pipe implementation that handles backpressure properly.

**Implementation steps:**
1. Create a buffered pipe implementation with configurable buffer size
2. Implement flow control to prevent buffer overflow
3. Use channels for coordination between read/write operations
4. Add timeout mechanisms to detect and break deadlocks

**Pros:**
- Maintains bidirectional communication
- Handles backpressure gracefully
- Can be tuned for performance

**Cons:**
- More complex implementation
- Requires careful testing

### Solution 2: Sequential Processing
Instead of concurrent bidirectional copying, process data sequentially.

**Implementation steps:**
1. Send all commands first
2. Wait for responses
3. Process responses one at a time
4. Close connection when done

**Pros:**
- Simple implementation
- No deadlock possible
- Easy to debug

**Cons:**
- May impact performance for interactive operations
- Changes the communication model

### Solution 3: Channel-Based Communication
Replace `io.Copy` with channel-based message passing.

**Implementation steps:**
1. Define message types for client-server communication
2. Use buffered channels for message passing
3. Implement proper channel closing semantics
4. Add message framing for proper boundaries

**Pros:**
- Go-idiomatic solution
- Clear message boundaries
- Easy to add features like timeouts

**Cons:**
- Requires refactoring handler interfaces
- May need protocol changes

### Solution 4: Non-Blocking I/O
Use non-blocking I/O operations with select statements.

**Implementation steps:**
1. Set handlers to non-blocking mode
2. Use select with timeouts for read/write operations
3. Implement proper EOF handling
4. Add retry logic for partial reads/writes

**Pros:**
- Fine-grained control over I/O
- Can detect and handle deadlocks

**Cons:**
- Complex error handling
- Platform-specific considerations

## Recommended Approach

Start with **Solution 1 (Buffered Pipe)** as it:
- Maintains the current architecture
- Provides a drop-in replacement for `io.Copy`
- Can be implemented incrementally
- Allows for performance tuning

## Implementation Plan

### Phase 1: Create Test Case
1. Write a test that reproduces the deadlock with a known file size
2. Ensure test fails consistently with current implementation
3. Add benchmarks to measure performance impact

### Phase 2: Implement Buffered Pipe
1. Create `internal/io/bufferedpipe` package
2. Implement `BufferedPipe` type with:
   - Configurable buffer size
   - Flow control mechanisms
   - Timeout support
3. Add comprehensive unit tests

### Phase 3: Integrate into Serverless Connector
1. Replace `io.Copy` calls with buffered pipe
2. Add configuration for buffer sizes
3. Implement graceful shutdown handling
4. Add metrics/logging for debugging

### Phase 4: Testing & Validation
1. Verify deadlock is resolved
2. Run performance benchmarks
3. Test with various file sizes
4. Ensure backward compatibility

### Phase 5: Documentation & Rollout
1. Update documentation
2. Add configuration examples
3. Create migration guide if needed
4. Monitor for issues in production

## Alternative Quick Fix

As an immediate mitigation, we could:
1. Detect serverless mode in profiling scenarios
2. Automatically add a dummy server to avoid serverless mode
3. Log a warning about the limitation

This would unblock profiling work while the proper fix is implemented.

## Success Criteria

1. No deadlocks with files of any size in serverless mode
2. Performance remains within 10% of current implementation
3. All existing tests pass
4. New tests verify the fix
5. Clear documentation of the solution