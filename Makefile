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
