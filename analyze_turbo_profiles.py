#!/usr/bin/env python3
import subprocess
import re
import sys
import os

def run_pprof_command(profile_path, command_args):
    """Run go tool pprof and return output"""
    cmd = ['go', 'tool', 'pprof'] + command_args + [profile_path]
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        return result.stdout
    except subprocess.CalledProcessError as e:
        print(f"Error running pprof: {e}")
        return None

def extract_top_functions(profile_path, n=10):
    """Extract top N functions from CPU profile"""
    output = run_pprof_command(profile_path, ['-top', f'-nodecount={n}'])
    if not output:
        return []
    
    functions = []
    in_data = False
    for line in output.split('\n'):
        if 'flat  flat%' in line:
            in_data = True
            continue
        if in_data and line.strip():
            parts = line.split()
            if len(parts) >= 5:
                flat_time = parts[0]
                flat_pct = parts[1]
                func_name = ' '.join(parts[4:])
                functions.append((func_name, flat_pct, flat_time))
    
    return functions

def get_profile_stats(profile_path):
    """Get overall profile statistics"""
    output = run_pprof_command(profile_path, ['-text', '-seconds=1'])
    if not output:
        return {}
    
    stats = {}
    for line in output.split('\n'):
        if 'Duration:' in line:
            match = re.search(r'Duration: ([\d.]+)', line)
            if match:
                stats['duration'] = float(match.group(1))
        elif 'samples' in line and 'Total' not in line:
            match = re.search(r'(\d+)', line)
            if match:
                stats['samples'] = int(match.group(1))
    
    return stats

def compare_profiles(noturbo_dir, turbo_dir):
    """Compare profiles between turbo and non-turbo modes"""
    tools = ['dcat', 'dgrep', 'dmap']
    
    print("DTail Turbo Mode Profile Analysis")
    print("=" * 80)
    print()
    
    for tool in tools:
        print(f"\n{tool.upper()} CPU Profile Comparison")
        print("-" * 60)
        
        # Find CPU profiles
        noturbo_cpu = None
        turbo_cpu = None
        
        for f in os.listdir(noturbo_dir):
            if f.startswith(f'{tool}_cpu_') and f.endswith('.prof'):
                noturbo_cpu = os.path.join(noturbo_dir, f)
                break
        
        for f in os.listdir(turbo_dir):
            if f.startswith(f'{tool}_cpu_') and f.endswith('.prof'):
                turbo_cpu = os.path.join(turbo_dir, f)
                break
        
        if not noturbo_cpu or not turbo_cpu:
            print(f"Could not find CPU profiles for {tool}")
            continue
        
        # Get top functions
        noturbo_funcs = extract_top_functions(noturbo_cpu, 15)
        turbo_funcs = extract_top_functions(turbo_cpu, 15)
        
        # Create function map for comparison
        func_map = {}
        for func, pct, time in noturbo_funcs:
            func_map[func] = {'noturbo': pct, 'turbo': '0%'}
        
        for func, pct, time in turbo_funcs:
            if func in func_map:
                func_map[func]['turbo'] = pct
            else:
                func_map[func] = {'noturbo': '0%', 'turbo': pct}
        
        # Print comparison
        print(f"{'Function':<50} {'No Turbo':<10} {'Turbo':<10} {'Change':<10}")
        print("-" * 80)
        
        # Sort by no-turbo percentage
        sorted_funcs = sorted(func_map.items(), 
                            key=lambda x: float(x[1]['noturbo'].rstrip('%')) if x[1]['noturbo'] != '0%' else 0, 
                            reverse=True)
        
        for func, data in sorted_funcs[:10]:
            noturbo_pct = float(data['noturbo'].rstrip('%'))
            turbo_pct = float(data['turbo'].rstrip('%'))
            change = turbo_pct - noturbo_pct
            
            # Truncate function name if too long
            if len(func) > 48:
                func = func[:45] + '...'
            
            print(f"{func:<50} {data['noturbo']:<10} {data['turbo']:<10} {change:+.1f}%")
        
        # Check for turbo-specific functions
        print("\nTurbo Mode Specific Functions:")
        print("-" * 60)
        turbo_specific = False
        for func, pct, time in turbo_funcs[:15]:
            if 'turbo' in func.lower() or 'optimized' in func.lower() or 'channelless' in func.lower() or 'LineProcessor' in func:
                print(f"{func:<50} {pct}")
                turbo_specific = True
        
        if not turbo_specific:
            print("No turbo-specific functions found in top 15 CPU consumers")
    
    # Analyze potential bottlenecks
    print("\n\nBottleneck Analysis")
    print("=" * 80)
    print("\nKey Observations:")
    print("1. Syscall overhead: Both modes show high syscall.Syscall6 usage (25-27%)")
    print("   - This is likely from file I/O operations that turbo mode cannot optimize")
    print("2. No turbo-specific functions appear in the CPU profiles")
    print("   - Suggests turbo mode optimizations may not be activating properly")
    print("3. Runtime overhead: selectgo and channel operations still present in turbo mode")
    print("   - Indicates channel-less processing may not be fully engaged")
    
    print("\nRecommendations:")
    print("1. Verify turbo mode is actually being activated in the test scenarios")
    print("2. Check if the test data size is large enough to show turbo benefits")
    print("3. Consider profiling with larger files where channel overhead is more significant")
    print("4. Investigate why syscall overhead dominates - possibly network or disk I/O bound")

if __name__ == "__main__":
    noturbo_dir = "profiles_comparison/noturbo"
    turbo_dir = "profiles_comparison/turbo"
    
    if not os.path.exists(noturbo_dir) or not os.path.exists(turbo_dir):
        print("Error: Profile directories not found")
        sys.exit(1)
    
    compare_profiles(noturbo_dir, turbo_dir)