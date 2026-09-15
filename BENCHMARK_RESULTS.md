# DTail Performance Benchmark Results

This document summarizes the performance comparison between the **Upstream** reference implementation and the **Local** optimized fork.

## Test Methodology
- **Cold Start**: The system page cache was dropped using `drop_caches` before every single observation to eliminate memory-caching bias.
- **Interleaved Runs**: Tests were run in alternating order (Upstream $\rightarrow$ Local $\rightarrow$ Upstream) to ensure fairness.
- **Modes**:
    - **Serverless (`sl_`)**: Direct disk access.
    - **Server (`sv_`)**: Communication via `dserver` over SSH.
- **Data Sets**: 100 MiB and 1 GiB deterministic log files.

## Summary Results

| Scenario | Samples | Upstream Median | Local Median | Speedup | Upstream MiB/s | Local MiB/s |
|---|---:|---:|---:|---:|---:|---:|
| **sl_dcat_large** (1 GiB) | 3 | 268.79 s | 1.86 s | **144.9x** | 3.81 | 551.95 |
| **sl_dcat_medium** (100 MiB) | 7 | 26.29 s | 0.25 s | **106.1x** | 3.80 | 403.42 |
| **sl_dgrep_high** (100 MiB) | 7 | 2.80 s | 0.17 s | **16.6x** | 35.71 | 591.29 |
| **sl_dgrep_low** (100 MiB) | 7 | 2.60 s | 0.21 s | **12.7x** | 38.50 | 487.69 |
| **sl_dmap_aggregate** (100 MiB)| 5 | 40.42 s | 1.90 s | **21.3x** | 2.47 | 52.67 |
| **sv_dcat_medium** (100 MiB) | 7 | 31.49 s | 8.61 s | **3.7x** | 3.18 | 11.61 |
| **sv_dgrep_high** (100 MiB) | 7 | 3.61 s | 1.13 s | **3.2x** | 27.73 | 88.61 |
| **sv_dgrep_low** (100 MiB) | 7 | 2.96 s | 0.35 s | **8.5x** | 33.76 | 286.09 |
| **sv_dmap_aggregate** (100 MiB)| 5 | 36.91 s | 2.14 s | **17.2x** | 2.71 | 46.66 |
| **sv_dmap_count** (100 MiB) | 5 | 37.46 s | 1.89 s | **19.8x** | 2.67 | 52.95 |
| **sv_dtail_follow** (10 MiB) | 3 | 4.02 s | 1.21 s | **3.3x** | 2.49 | 8.28 |

## Key Findings

### 1. Massive I/O Throughput Increase
In serverless `dcat` tests, the local version achieved speeds up to **552 MiB/s**, compared to the upstream version's **~3.8 MiB/s**. This represents a ~145x increase in raw read performance on large files.

### 2. Efficient MapReduce Processing
The MapReduce (`dmap`) scenarios showed some of the highest relative gains. Whether in serverless or server mode, the local version is **17x to 21x faster**, indicating that the bottlenecks in parsing and aggregation were successfully removed.

### 3. Sustained Gains in Server Mode
While the overall speed is lower in server mode due to SSH/network overhead, the local version still outperforms the upstream version by **3x to 20x**. The most significant gain in server mode was seen in `sv_dmap_count` (19.8x speedup).

## Conclusion
The local optimizations have successfully eliminated severe performance bottlenecks in the read path and the MapReduce engine. The improvements are consistent across different workloads (reading, searching, aggregating) and different transport modes (local vs. server).

## Stability and Repeatability
Two consecutive full benchmark runs were performed to verify consistency.

- **Local Version**: Demonstrated extreme stability with near-zero variance across runs (typically < 5% difference).
- **Upstream Version**: Showed significant volatility, particularly in MapReduce scenarios, where execution time increased by up to 65% in the second run despite identical conditions.

This confirms that the local optimizations not only provide a massive speed increase but also a more predictable and stable performance profile.
