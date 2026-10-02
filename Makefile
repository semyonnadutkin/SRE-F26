.PHONY: setup check up down cleanup audit
setup:
	python3 scripts/setup_env.py
check:
	@test -z "$$(gofmt -l cmd internal migrations)"
	go vet ./...
	go test ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'
up:
	docker compose up -d --build
down:
	docker compose down
cleanup:
	docker compose run --rm fileserver cleanup
audit:
	docker compose run --rm fileserver audit
