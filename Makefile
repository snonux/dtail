GO ?= go
ifdef DTAIL_USE_ACL
GO_TAGS=linuxacl
endif
ifdef DTAIL_USE_PROPRIETARY
GO_TAGS+=proprietary
endif
all: build
build: dserver dcat dgrep dmap dtail dtailhealth
dserver:
	${GO} build ${GO_FLAGS} -tags '${GO_TAGS}' -o dserver ./cmd/dserver/main.go
dcat:
	${GO} build ${GO_FLAGS} -tags '${GO_TAGS}' -o dcat ./cmd/dcat/main.go
dgrep:
	${GO} build ${GO_FLAGS} -tags '${GO_TAGS}' -o dgrep ./cmd/dgrep/main.go
dmap:
	${GO} build ${GO_FLAGS} -tags '${GO_TAGS}' -o dmap ./cmd/dmap/main.go
dtail:
	${GO} build ${GO_FLAGS} -tags '${GO_TAGS}' -o dtail ./cmd/dtail/main.go
dtailhealth:
	${GO} build ${GO_FLAGS} -tags '${GO_TAGS}' -o dtailhealth ./cmd/dtailhealth/main.go
install:
	${GO} install -tags '${GO_TAGS}' ./cmd/dserver/main.go
	${GO} install -tags '${GO_TAGS}' ./cmd/dcat/main.go
	${GO} install -tags '${GO_TAGS}' ./cmd/dgrep/main.go
	${GO} install -tags '${GO_TAGS}' ./cmd/dmap/main.go
	${GO} install -tags '${GO_TAGS}' ./cmd/dtail/main.go
	${GO} install -tags '${GO_TAGS}' ./cmd/dtailhealth/main.go
clean:
	ls ./cmd/ | while read cmd; do \
	  test -f $$cmd && rm $$cmd; \
	done
vet:
	find . -type d | egrep -v '(./examples|./log|./doc)' | while read dir; do \
	  echo ${GO} vet $$dir; \
	  ${GO} vet $$dir; \
	done
	sh -c 'grep -R NEXT: .'
	sh -c 'grep -R TODO: .'
lint:
	${GO} get golang.org/x/lint/golint
	find . -type d | while read dir; do \
	  echo golint $$dir; \
	  golint $$dir; \
	done | grep -F .go:
test:
	${GO} clean -testcache
	set -e; find . -name '*_test.go' | while read file; do dirname $$file; done | \
		sort -u | while read dir; do ${GO} test -tags '${GO_TAGS}' --race -v -failfast $$dir || exit 2; done
benchmark: build
	${GO} test -bench=. ./benchmarks
benchmark-quick: build
	${GO} test -bench=BenchmarkQuick ./benchmarks
benchmark-full: build
	${GO} test -bench=. -benchtime=3x ./benchmarks
benchmark-baseline: build
	@echo "Creating benchmark baseline..."
	@read -p "Enter a descriptive name for this baseline (e.g. 'before-optimization', 'v1.0-release'): " tag; \
	if [ -z "$$tag" ]; then \
		echo "Error: Baseline name cannot be empty"; \
		exit 1; \
	fi; \
	mkdir -p benchmarks/baselines; \
	filename="benchmarks/baselines/baseline_$$(date +%Y%m%d_%H%M%S)_$$(echo $$tag | tr ' ' '_' | tr -cd '[:alnum:]._-').txt"; \
	echo "Creating baseline: $$filename"; \
	echo "Git commit: $$(git rev-parse --short HEAD)" > "$$filename"; \
	echo "Date: $$(date)" >> "$$filename"; \
	echo "Tag: $$tag" >> "$$filename"; \
	echo "----------------------------------------" >> "$$filename"; \
	${GO} test -bench=. -benchmem ./benchmarks | tee -a "$$filename"; \
	echo "\nBaseline saved to: $$filename"
benchmark-baseline-quick: build
	@echo "Creating quick benchmark baseline..."
	@read -p "Enter a descriptive name for this baseline (e.g. 'before-optimization', 'v1.0-release'): " tag; \
	if [ -z "$$tag" ]; then \
		echo "Error: Baseline name cannot be empty"; \
		exit 1; \
	fi; \
	mkdir -p benchmarks/baselines; \
	filename="benchmarks/baselines/baseline_$$(date +%Y%m%d_%H%M%S)_$$(echo $$tag | tr ' ' '_' | tr -cd '[:alnum:]._-')_quick.txt"; \
	echo "Creating quick baseline: $$filename"; \
	echo "Git commit: $$(git rev-parse --short HEAD)" > "$$filename"; \
	echo "Date: $$(date)" >> "$$filename"; \
	echo "Tag: $$tag (quick)" >> "$$filename"; \
	echo "----------------------------------------" >> "$$filename"; \
	${GO} test -bench=BenchmarkQuick -benchmem ./benchmarks | tee -a "$$filename"; \
	echo "\nQuick baseline saved to: $$filename"
benchmark-compare: build
	@if [ -z "${BASELINE}" ]; then \
		echo "Usage: make benchmark-compare BASELINE=benchmarks/baselines/baseline_TIMESTAMP.txt"; \
		echo "Available baselines:"; \
		ls -1 benchmarks/baselines/*.txt 2>/dev/null || echo "  No baselines found"; \
		exit 1; \
	fi
	@echo "Running current benchmarks and comparing with ${BASELINE}..."
	${GO} test -bench=. -benchmem ./benchmarks | tee benchmarks/baselines/current.txt
	@echo "\n=== Comparison Report ==="
	@if command -v benchstat >/dev/null 2>&1; then \
		benchstat ${BASELINE} benchmarks/baselines/current.txt; \
	else \
		echo "benchstat not found. Install with: go install golang.org/x/perf/cmd/benchstat@latest"; \
		echo "\nShowing simple diff instead:"; \
		diff -u ${BASELINE} benchmarks/baselines/current.txt || true; \
	fi

# Profiling targets
PROFILE_DIR ?= profiles
PROFILE_SIZE ?= 1000000  # Default 1M lines for profiling

# Generate test data for profiling
profile-testdata:
	@echo "Generating test data for profiling..."
	@mkdir -p testdata
	@echo "Creating testdata/profile_test.log (${PROFILE_SIZE} lines)..."
	@seq 1 ${PROFILE_SIZE} | while read i; do \
		echo "[2024-01-01 00:00:$$i] INFO - Processing request $$i from user$$(($$i % 100)) with status $$(($$i % 2))"; \
	done > testdata/profile_test.log
	@echo "Creating testdata/profile_test.csv..."
	@echo "timestamp,user,action,duration,status" > testdata/profile_test.csv
	@seq 1 $$(( ${PROFILE_SIZE} / 10 )) | while read i; do \
		echo "2024-01-01 00:00:$$i,user$$(($$i % 100)),$$([ $$(($$i % 3)) -eq 0 ] && echo login || [ $$(($$i % 3)) -eq 1 ] && echo query || echo logout),$$((100 + $$i % 900)),$$([ $$(($$i % 2)) -eq 0 ] && echo success || echo failure)"; \
	done >> testdata/profile_test.csv
	@echo "Test data generated in testdata/"

# Profile dcat
profile-dcat: dcat profile-testdata
	@echo "Profiling dcat..."
	@mkdir -p ${PROFILE_DIR}
	@echo "Command: ./dcat -profile -profiledir ${PROFILE_DIR} -plain -cfg none testdata/profile_test.log"
	./dcat -profile -profiledir ${PROFILE_DIR} -plain -cfg none testdata/profile_test.log > /dev/null
	@echo "\nAnalyzing dcat profiles..."
	@echo "CPU Profile:"
	@echo "Command: ./profiling/profile.sh -top 5 ${PROFILE_DIR}/dcat_cpu_*.prof"
	@./profiling/profile.sh -top 5 ${PROFILE_DIR}/dcat_cpu_*.prof | tail -n +3
	@echo "\nMemory Profile:"
	@echo "Command: ./profiling/profile.sh -top 5 ${PROFILE_DIR}/dcat_mem_*.prof"
	@./profiling/profile.sh -top 5 ${PROFILE_DIR}/dcat_mem_*.prof | tail -n +3

# Profile dgrep
profile-dgrep: dgrep profile-testdata
	@echo "Profiling dgrep..."
	@mkdir -p ${PROFILE_DIR}
	@echo "Command: ./dgrep -profile -profiledir ${PROFILE_DIR} -plain -cfg none -regex \"ERROR|user[0-9]+\" testdata/profile_test.log"
	./dgrep -profile -profiledir ${PROFILE_DIR} -plain -cfg none -regex "ERROR|user[0-9]+" testdata/profile_test.log > /dev/null
	@echo "\nAnalyzing dgrep profiles..."
	@echo "CPU Profile:"
	@echo "Command: ./profiling/profile.sh -top 5 ${PROFILE_DIR}/dgrep_cpu_*.prof"
	@./profiling/profile.sh -top 5 ${PROFILE_DIR}/dgrep_cpu_*.prof | tail -n +3
	@echo "\nMemory Profile:"
	@echo "Command: ./profiling/profile.sh -top 5 ${PROFILE_DIR}/dgrep_mem_*.prof"
	@./profiling/profile.sh -top 5 ${PROFILE_DIR}/dgrep_mem_*.prof | tail -n +3

# Profile dmap (with MapReduce format data)
profile-dmap: dmap
	@echo "Profiling dmap with MapReduce format..."
	@cd profiling && ./profile_dmap.sh

# Profile all commands
profile-all: profile-dcat profile-dgrep profile-dmap
	@echo "\nAll profiling complete. Profiles saved in ${PROFILE_DIR}/"

# Interactive profile analysis
profile-analyze:
	@if [ -z "${PROFILE}" ]; then \
		echo "Available profiles:"; \
		ls -1t ${PROFILE_DIR}/*.prof 2>/dev/null | head -20 || echo "  No profiles found in ${PROFILE_DIR}/"; \
		echo ""; \
		echo "Usage: make profile-analyze PROFILE=profiles/dcat_cpu_*.prof"; \
	else \
		echo "Opening interactive pprof for ${PROFILE}..."; \
		go tool pprof ${PROFILE}; \
	fi

# Generate flame graph
profile-flamegraph:
	@if [ -z "${PROFILE}" ]; then \
		echo "Usage: make profile-flamegraph PROFILE=profiles/dcat_cpu_*.prof"; \
		echo ""; \
		echo "Available CPU profiles:"; \
		ls -1t ${PROFILE_DIR}/*_cpu_*.prof 2>/dev/null | head -10 || echo "  No CPU profiles found"; \
	else \
		echo "Starting pprof web server for ${PROFILE}..."; \
		echo "Open http://localhost:8080 in your browser"; \
		echo "Press Ctrl+C to stop"; \
		go tool pprof -http=:8080 ${PROFILE}; \
	fi

# Clean profiles
profile-clean:
	@echo "Cleaning profile directory..."
	rm -rf ${PROFILE_DIR}
	@echo "Profile directory cleaned"

# Run profiling benchmarks
profile-benchmark: dcat dgrep dmap
	@echo "Running profiling benchmarks..."
	cd benchmarks && ${GO} test -bench="WithProfiling" -benchtime=1x -v

# Run automated profiling script
profile-auto: dcat dgrep dmap
	@echo "Running automated profiling script..."
	cd profiling && ./profile_benchmarks.sh

# Run quick profiling (smaller datasets)
profile-quick: dcat dgrep dmap
	@echo "Running quick profiling..."
	cd profiling && ./profile_quick.sh

# Show profiling help
profile-help:
	@echo "DTail Profiling Targets:"
	@echo ""
	@echo "  make profile-all          - Profile all commands (dcat, dgrep, dmap)"
	@echo "  make profile-dcat         - Profile dcat command"
	@echo "  make profile-dgrep        - Profile dgrep command"
	@echo "  make profile-dmap         - Profile dmap command"
	@echo ""
	@echo "  make profile-quick        - Quick profiling with small datasets"
	@echo "  make profile-auto         - Full automated profiling (includes large files)"
	@echo ""
	@echo "  make profile-analyze      - Interactive profile analysis"
	@echo "    Example: make profile-analyze PROFILE=profiles/dcat_cpu_*.prof"
	@echo ""
	@echo "  make profile-flamegraph   - Generate flame graph visualization"
	@echo "    Example: make profile-flamegraph PROFILE=profiles/dcat_cpu_*.prof"
	@echo ""
	@echo "  make profile-benchmark    - Run profiling benchmarks"
	@echo "  make profile-clean        - Clean all profiles"
	@echo ""
	@echo "Options:"
	@echo "  PROFILE_DIR=<dir>         - Profile output directory (default: profiles)"
	@echo "  PROFILE_SIZE=<lines>      - Test data size in lines (default: 1000000)"
	@echo ""
	@echo "Examples:"
	@echo "  make profile-all PROFILE_SIZE=10000000    # Profile with 10M lines"
	@echo "  make profile-dcat PROFILE_DIR=myprofiles  # Custom profile directory"
	@echo ""
	@echo "Quick start:"
	@echo "  make profile-quick        # Fast profiling with immediate results"

.PHONY: profile-testdata profile-dcat profile-dgrep profile-dmap profile-all profile-analyze profile-flamegraph profile-clean profile-benchmark profile-auto profile-quick profile-help
