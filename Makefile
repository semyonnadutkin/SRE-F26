.PHONY: setup check up down cleanup audit k8s-init k8s-apply

KUBECTL ?= minikube kubectl --

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

# Prepare dependencies before starting one-off database/storage initialization.
k8s-init:
	python3 scripts/setup_k8s.py
	$(KUBECTL) apply -f k8s/namespace.yaml
	$(KUBECTL) apply -k k8s/postgres
	$(KUBECTL) apply -k k8s/s3
	$(KUBECTL) apply -f k8s/fileserver/configmap.yaml -f k8s/fileserver/secret.yaml
	$(KUBECTL) -n fileserver rollout status statefulset/postgres --timeout=300s
	$(KUBECTL) -n fileserver rollout status statefulset/s3 --timeout=300s
	$(KUBECTL) -n fileserver delete job fileserver-migrate fileserver-bucket-init --ignore-not-found --wait=true
	$(KUBECTL) apply -f k8s/jobs/fileserver-migrate.yaml -f k8s/jobs/fileserver-bucket-init.yaml
	$(KUBECTL) -n fileserver wait --for=condition=complete job/fileserver-migrate job/fileserver-bucket-init --timeout=600s

# Restart to pick up ConfigMap/Secret env changes, then wait for the new pods.
k8s-apply: k8s-init
	$(KUBECTL) apply -k k8s
	$(KUBECTL) -n fileserver rollout restart deployment/fileserver-deployment
	$(KUBECTL) -n fileserver rollout status deployment/fileserver-deployment --timeout=300s
	$(KUBECTL) -n observability rollout status deployment/victorialogs --timeout=300s
	$(KUBECTL) -n observability rollout status daemonset/vector --timeout=300s
	$(KUBECTL) -n observability rollout status deployment/grafana --timeout=300s
