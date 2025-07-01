#!/usr/bin/env python3
import re
import sys

def parse_benchmark_results(filename):
    results = {}
    with open(filename, 'r') as f:
        content = f.read()
        
    # Parse benchmark lines
    pattern = r'Benchmark(\w+)/(\w+)/([^-]+)-\d+\s+\d+\s+([\d.]+) ns/op\s+([\d.]+) MB/sec(?:\s+([\d.]+) hit_rate_%)?(?:\s+([\d.]+) lines/sec)?(?:\s+([\d.]+) records/sec)?'
    
    for match in re.finditer(pattern, content):
        test_type = match.group(1)
        command = match.group(2)
        params = match.group(3)
        ns_per_op = float(match.group(4))
        mb_per_sec = float(match.group(5))
        hit_rate = float(match.group(6)) if match.group(6) else None
        lines_per_sec = float(match.group(7)) if match.group(7) else None
        records_per_sec = float(match.group(8)) if match.group(8) else None
        
        key = f"{test_type}/{command}/{params}"
        results[key] = {
            'ns_per_op': ns_per_op,
            'mb_per_sec': mb_per_sec,
            'hit_rate': hit_rate,
            'lines_per_sec': lines_per_sec,
            'records_per_sec': records_per_sec
        }
    
    return results

def calculate_improvement(no_turbo, turbo):
    if no_turbo == 0:
        return 0
    return ((no_turbo - turbo) / no_turbo) * 100

def main():
    no_turbo_results = parse_benchmark_results('benchmark_noturbo.txt')
    turbo_results = parse_benchmark_results('benchmark_turbo.txt')
    
    print("DTail Turbo Mode Benchmark Comparison")
    print("=" * 80)
    print()
    
    # Group by command
    commands = {}
    for key in no_turbo_results.keys():
        if key in turbo_results:
            parts = key.split('/')
            command = parts[1]
            if command not in commands:
                commands[command] = []
            commands[command].append(key)
    
    for command in sorted(commands.keys()):
        print(f"\n{command} Benchmarks:")
        print("-" * 60)
        print(f"{'Test':<40} {'Metric':<15} {'No Turbo':<12} {'Turbo':<12} {'Improvement':<12}")
        print("-" * 60)
        
        for key in sorted(commands[command]):
            no_turbo = no_turbo_results[key]
            turbo = turbo_results[key]
            
            test_name = key.split('/', 2)[2]
            
            # Time improvement (lower is better)
            time_imp = calculate_improvement(no_turbo['ns_per_op'], turbo['ns_per_op'])
            print(f"{test_name:<40} {'Time (ns/op)':<15} {no_turbo['ns_per_op']:<12.0f} {turbo['ns_per_op']:<12.0f} {time_imp:>10.1f}%")
            
            # Throughput improvement (higher is better)
            mb_imp = calculate_improvement(turbo['mb_per_sec'], no_turbo['mb_per_sec'])
            print(f"{'':<40} {'MB/sec':<15} {no_turbo['mb_per_sec']:<12.2f} {turbo['mb_per_sec']:<12.2f} {-mb_imp:>10.1f}%")
            
            if no_turbo['lines_per_sec']:
                lines_imp = calculate_improvement(turbo['lines_per_sec'], no_turbo['lines_per_sec'])
                print(f"{'':<40} {'Lines/sec':<15} {no_turbo['lines_per_sec']:<12.0f} {turbo['lines_per_sec']:<12.0f} {-lines_imp:>10.1f}%")
            
            if no_turbo['records_per_sec']:
                records_imp = calculate_improvement(turbo['records_per_sec'], no_turbo['records_per_sec'])
                print(f"{'':<40} {'Records/sec':<15} {no_turbo['records_per_sec']:<12.0f} {turbo['records_per_sec']:<12.0f} {-records_imp:>10.1f}%")
            
            print()
    
    # Summary statistics
    print("\nSummary:")
    print("-" * 60)
    
    total_time_improvement = []
    for key in no_turbo_results.keys():
        if key in turbo_results:
            imp = calculate_improvement(no_turbo_results[key]['ns_per_op'], turbo_results[key]['ns_per_op'])
            total_time_improvement.append(imp)
    
    if total_time_improvement:
        avg_improvement = sum(total_time_improvement) / len(total_time_improvement)
        print(f"Average time improvement: {avg_improvement:.1f}%")
        print(f"Best improvement: {max(total_time_improvement):.1f}%")
        print(f"Worst improvement: {min(total_time_improvement):.1f}%")
    
    print("\nNote: Positive improvements mean turbo mode is faster/better.")
    print("      Negative improvements mean turbo mode is slower/worse.")

if __name__ == "__main__":
    main()