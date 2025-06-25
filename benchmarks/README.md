# DTail Benchmarks

This directory contains comprehensive benchmarks for the DTail toolset (dcat, dgrep, dmap).

## Overview

The benchmarking framework tests performance across:
- Different file sizes (10MB, 100MB, 1GB)
- Various compression formats (none, gzip, zstd)
- Different query patterns and complexities
- Server mode vs serverless operation

## Prerequisites

Before running benchmarks, ensure all DTail binaries are built:

```bash
cd ..
make build
```

## Running Benchmarks

### Quick Benchmarks (Small Files Only)
```bash
go test -bench=BenchmarkQuick ./benchmarks
```

### All Benchmarks
```bash
go test -bench=. ./benchmarks
```

### Specific Tool Benchmarks
```bash
# DCat benchmarks only
go test -bench=BenchmarkDCat ./benchmarks

# DGrep benchmarks only
go test -bench=BenchmarkDGrep ./benchmarks

# DMap benchmarks only
go test -bench=BenchmarkDMap ./benchmarks
```

### With Memory Profiling
```bash
go test -bench=. -benchmem ./benchmarks
```

### Custom Configuration
```bash
# Run with specific file sizes
DTAIL_BENCH_SIZES=small,medium go test -bench=. ./benchmarks

# Keep temporary files for inspection
DTAIL_BENCH_KEEP_FILES=true go test -bench=. ./benchmarks

# Set custom timeout
DTAIL_BENCH_TIMEOUT=30m go test -bench=. ./benchmarks
```

## Benchmark Categories

### DCat Benchmarks
- **Simple**: Sequential file reading
- **Multiple Files**: Reading 10-100 files concurrently
- **Compressed**: Performance with gzip/zstd compression
- **Server Mode**: Client-server performance comparison

### DGrep Benchmarks
- **Simple Pattern**: Basic string matching with varying hit rates
- **Regex Pattern**: Complex regex performance
- **Context Lines**: Impact of --before/--after flags
- **Inverted**: Performance of --invert grep
- **Compressed**: Grep on compressed files

### DMap Benchmarks
- **Simple Aggregation**: Basic count, sum, avg operations
- **Group By Cardinality**: Performance with different group sizes
- **Complex Queries**: WHERE clauses and multiple conditions
- **Time Intervals**: Time-based grouping performance
- **Custom Functions**: Performance of maskdigits, md5sum, etc.

## Output

Benchmark results are saved in multiple formats:
- `benchmark_results/results_TIMESTAMP.json` - Machine-readable JSON
- `benchmark_results/results_TIMESTAMP.csv` - Spreadsheet-compatible CSV
- `benchmark_results/results_TIMESTAMP.md` - Human-readable Markdown report
- `benchmark_results/latest.json` - Most recent results for easy access

## Interpreting Results

Key metrics:
- **MB/sec**: Throughput in megabytes per second
- **lines/sec**: Lines processed per second
- **compression_ratio**: For compressed file benchmarks
- **matched_lines**: For grep benchmarks
- **approx_groups**: For MapReduce group by operations

## Performance Tuning

For accurate benchmarks:
1. Run on isolated hardware
2. Disable CPU frequency scaling
3. Close unnecessary applications
4. Run multiple times and average results

## Continuous Integration

The benchmarks can be integrated into CI/CD pipelines:

```yaml
# Example GitHub Actions workflow
- name: Run Benchmarks
  run: |
    make build
    go test -bench=BenchmarkQuick ./benchmarks
```

## Troubleshooting

### "Command not found" errors
Ensure DTail binaries are built: `make build`

### Disk space issues
Benchmarks create large temporary files. Ensure sufficient disk space (>2GB).

### Timeout errors
Increase timeout: `DTAIL_BENCH_TIMEOUT=60m go test -bench=. ./benchmarks`

## Contributing

When adding new benchmarks:
1. Follow existing naming conventions
2. Include warmup runs
3. Report relevant metrics
4. Clean up temporary files
5. Document in this README