.PHONY: test build smoke

test:
	$(MAKE) -C server test
	cd node && cargo fmt --check && cargo clippy -- -D warnings && cargo test
	cd frontend && npm run check && npm test

build:
	$(MAKE) -C server build
	cd node && cargo build --release
	cd frontend && npm run build

smoke: build
	./scripts/smoke-m1.sh
