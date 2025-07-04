GO ?= go
ifdef DTAIL_USE_ACL
GO_TAGS=linuxacl
endif
ifdef DTAIL_USE_PROPRIETARY
GO_TAGS+=proprietary
endif
all: build
build: dserver dcat dgrep dmap dtail dtailhealth dtail-tools
build-pgo: pgo-build-binaries
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
dtail-tools:
	${GO} build ${GO_FLAGS} -tags '${GO_TAGS}' -o dtail-tools ./cmd/dtail-tools/main.go
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
	@echo "Removing .tmp files..."
	find . -name "*.tmp" -type f -delete
	@echo "Removing .prof files..."
	find . -name "*.prof" -type f -delete
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
benchmark: build dtail-tools
	./dtail-tools benchmark -mode run
benchmark-quick: build dtail-tools
	./dtail-tools benchmark -mode run -quick
benchmark-full: build dtail-tools
	./dtail-tools benchmark -mode run -iterations 3x
benchmark-baseline: build dtail-tools
	@read -p "Enter a descriptive name for this baseline (e.g. 'before-optimization', 'v1.0-release'): " tag; \
	if [ -z "$$tag" ]; then \
		echo "Error: Baseline name cannot be empty"; \
		exit 1; \
	fi; \
	./dtail-tools benchmark -mode baseline -tag "$$tag"
benchmark-baseline-quick: build dtail-tools
	@read -p "Enter a descriptive name for this baseline (e.g. 'before-optimization', 'v1.0-release'): " tag; \
	if [ -z "$$tag" ]; then \
		echo "Error: Baseline name cannot be empty"; \
		exit 1; \
	fi; \
	./dtail-tools benchmark -mode baseline -tag "$$tag" -quick
benchmark-compare: build dtail-tools
	@if [ -z "${BASELINE}" ]; then \
		echo "Usage: make benchmark-compare BASELINE=benchmarks/baselines/baseline_TIMESTAMP.txt"; \
		./dtail-tools benchmark -mode list; \
		exit 1; \
	fi
	./dtail-tools benchmark -mode compare -baseline ${BASELINE}

# Profiling targets
profile-all: build dtail-tools
	./dtail-tools profile -mode full
profile-quick: build dtail-tools
	./dtail-tools profile -mode quick
profile-dmap: build dtail-tools
	./dtail-tools profile -mode dmap
profile-list: dtail-tools
	./dtail-tools profile -mode list

# Interactive profile analysis
profile-analyze: dtail-tools
	@if [ -z "${PROFILE}" ]; then \
		echo "Usage: make profile-analyze PROFILE=profiles/dcat_cpu_*.prof"; \
		./dtail-tools profile -mode list; \
	else \
		./dtail-tools profile -mode analyze ${PROFILE}; \
	fi

# Generate flame graph (web interface)
profile-web: dtail-tools
	@if [ -z "${PROFILE}" ]; then \
		echo "Usage: make profile-web PROFILE=profiles/dcat_cpu_*.prof"; \
		./dtail-tools profile -mode list; \
	else \
		./dtail-tools profile -mode analyze ${PROFILE} -web; \
	fi

# Clean profiles
profile-clean:
	@echo "Cleaning profile directory..."
	rm -rf profiles testdata
	@echo "Profile directory cleaned"

# Show profiling help
profile-help:
	@echo "DTail Profiling Targets:"
	@echo ""
	@echo "  make profile-quick        - Quick profiling with small datasets"
	@echo "  make profile-all          - Full profiling suite"
	@echo "  make profile-dmap         - Profile dmap specifically"
	@echo "  make profile-list         - List available profiles"
	@echo ""
	@echo "  make profile-analyze PROFILE=<file>  - Analyze a specific profile"
	@echo "  make profile-web PROFILE=<file>      - Open web interface for profile"
	@echo ""
	@echo "  make profile-clean        - Clean all profiles"
	@echo ""
	@echo "Examples:"
	@echo "  make profile-quick                    # Fast profiling"
	@echo "  make profile-analyze PROFILE=profiles/dcat_cpu_*.prof"
	@echo ""

.PHONY: profile-all profile-quick profile-dmap profile-list profile-analyze profile-web profile-clean profile-help

## Profile-Guided Optimization targets
pgo: build dtail-tools
	@echo "Running Profile-Guided Optimization for all commands..."
	./dtail-tools pgo

pgo-quick: build dtail-tools
	@echo "Running quick PGO with smaller datasets..."
	./dtail-tools pgo -datasize 100000 -iterations 2

pgo-commands: build dtail-tools
	@if [ -z "${COMMANDS}" ]; then \
		echo "Usage: make pgo-commands COMMANDS='dcat dgrep'"; \
		exit 1; \
	fi
	./dtail-tools pgo ${COMMANDS}

pgo-clean:
	@echo "Cleaning PGO artifacts..."
	rm -rf pgo-profiles pgo-build

pgo-help:
	@echo "DTail PGO (Profile-Guided Optimization) Targets:"
	@echo ""
	@echo "  make pgo              - Run PGO for all commands (full optimization)"
	@echo "  make pgo-quick        - Quick PGO with smaller datasets"
	@echo "  make pgo-commands     - PGO for specific commands"
	@echo "                          Example: make pgo-commands COMMANDS='dcat dgrep'"
	@echo "  make pgo-clean        - Remove PGO artifacts"
	@echo ""
	@echo "After running PGO, optimized binaries will be in pgo-build/"
	@echo ""

# Build PGO-optimized binaries without running benchmarks
# This assumes PGO profiles already exist in pgo-profiles/
pgo-build-binaries: dtail-tools
	@if [ ! -d "pgo-profiles" ]; then \
		echo "Error: pgo-profiles directory not found."; \
		echo "Run 'make pgo' first to generate profiles, or 'make pgo-generate' to only generate profiles."; \
		exit 1; \
	fi
	@echo "Building PGO-optimized binaries using existing profiles..."
	@mkdir -p pgo-build
	@for cmd in dcat dgrep dmap dtail dserver; do \
		profile="pgo-profiles/$$cmd.pprof"; \
		if [ -f "$$profile" ]; then \
			echo "Building $$cmd with PGO..."; \
			${GO} build ${GO_FLAGS} -tags '${GO_TAGS}' -pgo=$$profile -o pgo-build/$$cmd ./cmd/$$cmd/main.go; \
		else \
			echo "Warning: Profile $$profile not found, building without PGO..."; \
			${GO} build ${GO_FLAGS} -tags '${GO_TAGS}' -o pgo-build/$$cmd ./cmd/$$cmd/main.go; \
		fi \
	done
	@echo "PGO-optimized binaries built in pgo-build/"

# Generate PGO profiles without building optimized binaries
pgo-generate: build dtail-tools
	@echo "Generating PGO profiles..."
	./dtail-tools pgo -profileonly
	@echo "PGO profiles generated in pgo-profiles/"

# Install PGO-optimized binaries to system
install-pgo: pgo-build-binaries
	@echo "Installing PGO-optimized binaries..."
	@for cmd in dcat dgrep dmap dtail dserver; do \
		if [ -f "pgo-build/$$cmd" ]; then \
			echo "Installing $$cmd..."; \
			cp pgo-build/$$cmd ${GOPATH}/bin/$$cmd || sudo cp pgo-build/$$cmd /usr/local/bin/$$cmd; \
		fi \
	done
	@echo "PGO-optimized binaries installed"

.PHONY: pgo pgo-quick pgo-commands pgo-clean pgo-help pgo-build-binaries pgo-generate install-pgo
