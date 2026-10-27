.PHONY: setup check up down cleanup audit k8s-init k8s-apply k8s-victoriametrics k8s-update-image

KUBECTL ?= minikube kubectl --
HELM ?= helm

# VictoriaMetrics-related variables
VM_CHART_VERSION := 0.49.0
VM_VALUES_DIR := k8s/observability/victoriametrics

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

k8s-victoriametrics:
	$(HELM) repo add vm https://victoriametrics.github.io/helm-charts/
	$(HELM) repo update vm
	$(KUBECTL) apply -f k8s/observability/namespace.yaml
	$(HELM) upgrade --install vmsingle vm/victoria-metrics-single \
		--version "$(VM_CHART_VERSION)" \
		--kube-context minikube \
		--namespace observability \
		--reset-values \
		-f "$(VM_VALUES_DIR)/vmsingle-values.yaml" \
		-f "$(VM_VALUES_DIR)/values-nodeport.yaml" \
		--wait \
		--timeout 5m

k8s-update-image:
	docker build -t sre-f26-fileserver:local .
	minikube image load -p minikube sre-f26-fileserver:local --overwrite=true
	$(KUBECTL) -n fileserver rollout restart deployment/fileserver-deployment
	$(KUBECTL) -n fileserver rollout status deployment/fileserver-deployment --timeout=300s

# Restart to pick up ConfigMap/Secret env changes, then wait for the new pods.
k8s-apply: k8s-init
	$(KUBECTL) apply -k k8s
	$(KUBECTL) -n fileserver rollout restart deployment/fileserver-deployment
	$(KUBECTL) -n fileserver rollout status deployment/fileserver-deployment --timeout=300s
	$(KUBECTL) -n observability rollout status deployment/victorialogs --timeout=300s
	$(KUBECTL) -n observability rollout status daemonset/vector --timeout=300s
	$(KUBECTL) -n observability rollout status deployment/grafana --timeout=300s
	$(MAKE) k8s-victoriametrics
