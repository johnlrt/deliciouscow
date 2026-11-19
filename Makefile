.PHONY: test build smoke config-check contracts-check dev-up dev-status dev-logs dev-smoke dev-down dev-reset

config-check:
	bash scripts/config-check.sh

contracts-check:
	node scripts/contracts.mjs check

test: config-check contracts-check
	$(MAKE) -C server test
	go build -C server ./...
	cd node && cargo fmt --check && cargo clippy -- -D warnings && cargo test
	cd frontend && npm run check && npm test

build:
	$(MAKE) -C server build
	cd node && cargo build --release
	cd frontend && npm run build

smoke: build
	./scripts/smoke-m1.sh

dev-up:
	./scripts/cordova-dev.sh up

dev-status:
	./scripts/cordova-dev.sh status

dev-logs:
	./scripts/cordova-dev.sh logs

dev-smoke:
	./scripts/cordova-dev.sh smoke

dev-down:
	./scripts/cordova-dev.sh down

dev-reset:
	./scripts/cordova-dev.sh reset
