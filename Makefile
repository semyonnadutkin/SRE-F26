.PHONY: check probe maintenance-cleanup maintenance-audit \
        helm-repos k8s-bootstrap k8s-deploy k8s-update-image \
        k8s-deploy-victorialogs k8s-deploy-vector \
        k8s-deploy-grafana k8s-deploy-victoriametrics \
        k8s-deploy-alertmanager k8s-deploy-vmalert

KUBECTL ?= minikube kubectl --
HELM ?= helm
PYTHON ?= .venv/bin/python

# Prober-related values
MODE ?= normal
TARGET_URL ?= 127.0.0.1
export TARGET_URL

# Grafana-related values
GF_CHART_VERSION := 13.5.0
GF_DIR := k8s/observability/grafana

# VictoriaLogs-related values
VL_CHART_VERSION := 0.13.10
VL_VALUES_FILE := k8s/observability/victoria-logs/values.yaml

# Vector-related values
VECTOR_CHART_VERSION := 0.49.0
VECTOR_VALUES_FILE := k8s/observability/vector/values.yaml

# VictoriaMetrics-related values
VM_CHART_VERSION := 0.49.0
VM_VALUES_DIR := k8s/observability/victoria-metrics

# Alertmanager-related values
AM_CHART_VERSION := 2.1.2
AM_VALUES_FILE := k8s/observability/alertmanager/values.yaml

# VMAlert-related values
VMA_CHART_VERSION := 0.50.0
VMA_VALUES_DIR := k8s/observability/vmalert

check:
	@test -z "$$(gofmt -l cmd internal migrations)"
	go vet ./...
	go test ./...
	$(PYTHON) -m unittest discover -s scripts -p 'test_*.py'

probe:
	$(PYTHON) scripts/prober/main.py $(MODE)

# Run cleanup immediately using the existing CronJob template.
maintenance-cleanup:
	@set -eu; \
	job="fileserver-cleanup-manual-$$(date +%s)-$$$$"; \
	$(KUBECTL) -n fileserver create job "$$job" \
		--from=cronjob/fileserver-cleanup-cron; \
	status=0; \
	$(KUBECTL) -n fileserver wait \
		--for=condition=complete "job/$$job" \
		--timeout=960s || status=$$?; \
	$(KUBECTL) -n fileserver logs "job/$$job" || true; \
	if [ "$$status" -eq 0 ]; then \
		$(KUBECTL) -n fileserver delete "job/$$job" --wait=false; \
	fi; \
	exit "$$status"

# Create a new one-off audit Job and display its logs.
maintenance-audit:
	@set -eu; \
	job="$$( $(KUBECTL) -n fileserver create \
		-f k8s/jobs/fileserver-audit.yaml -o name )"; \
	status=0; \
	$(KUBECTL) -n fileserver wait \
		--for=condition=complete "$$job" \
		--timeout=660s || status=$$?; \
	$(KUBECTL) -n fileserver logs "$$job" || true; \
	exit "$$status"

# Prepare databases, storage and initialization Jobs.
k8s-bootstrap:
	$(PYTHON) scripts/setup_k8s.py
	$(KUBECTL) apply -f k8s/namespace.yaml
	$(KUBECTL) apply -k k8s/postgres
	$(KUBECTL) apply -k k8s/s3
	$(KUBECTL) apply -f k8s/fileserver/configmap.yaml \
		-f k8s/fileserver/secret.yaml
	$(KUBECTL) -n fileserver rollout status statefulset/postgres --timeout=300s
	$(KUBECTL) -n fileserver rollout status statefulset/s3 --timeout=300s
	$(KUBECTL) -n fileserver delete job \
		fileserver-migrate fileserver-bucket-init \
		--ignore-not-found --wait=true
	$(KUBECTL) apply -f k8s/jobs/fileserver-migrate.yaml \
		-f k8s/jobs/fileserver-bucket-init.yaml
	$(KUBECTL) -n fileserver wait --for=condition=complete \
		job/fileserver-migrate job/fileserver-bucket-init \
		--timeout=600s

# Prepare the chart repositories once per Make invocation.
helm-repos:
	$(HELM) repo add vm https://victoriametrics.github.io/helm-charts/
	$(HELM) repo add vector https://helm.vector.dev
	$(HELM) repo add grafana-community https://grafana-community.github.io/helm-charts
	$(HELM) repo add prometheus-community https://prometheus-community.github.io/helm-charts
	$(HELM) repo update

k8s-deploy-victorialogs k8s-deploy-vector k8s-deploy-victoriametrics k8s-deploy-grafana k8s-deploy-alertmanager k8s-deploy-vmalert: helm-repos

k8s-deploy-victorialogs:
	$(KUBECTL) apply -f k8s/observability/namespace.yaml
	$(HELM) upgrade --install victorialogs vm/victoria-logs-single \
		--version "$(VL_CHART_VERSION)" \
		--kube-context minikube \
		--namespace observability \
		--reset-values \
		-f "$(VL_VALUES_FILE)" \
		--rollback-on-failure --wait --timeout 5m

k8s-deploy-vector:
	$(KUBECTL) apply -f k8s/observability/namespace.yaml
	$(HELM) upgrade --install vector vector/vector \
		--version "$(VECTOR_CHART_VERSION)" \
		--kube-context minikube \
		--namespace observability \
		--reset-values \
		-f "$(VECTOR_VALUES_FILE)" \
		--rollback-on-failure --wait --timeout 5m

k8s-deploy-victoriametrics:
	$(KUBECTL) apply -f k8s/observability/namespace.yaml
	$(HELM) upgrade --install vmsingle vm/victoria-metrics-single \
		--version "$(VM_CHART_VERSION)" \
		--kube-context minikube \
		--namespace observability \
		--reset-values \
		-f "$(VM_VALUES_DIR)/values.yaml" \
		--rollback-on-failure --wait --timeout 5m

k8s-deploy-grafana:
	$(KUBECTL) apply -f k8s/observability/namespace.yaml
	$(HELM) upgrade --install grafana grafana-community/grafana \
		--version "$(GF_CHART_VERSION)" \
		--kube-context minikube \
		--namespace observability \
		--reset-values \
		-f "$(GF_DIR)/values.yaml" \
		--set-file dashboards.fileserver.fileserver-logs.json="$(GF_DIR)/dashboards/fileserver-logs.json" \
		--set-file dashboards.fileserver.fileserver-slo.json="$(GF_DIR)/dashboards/fileserver-slo.json" \
		--rollback-on-failure --wait --timeout 5m

k8s-deploy-alertmanager:
	$(PYTHON) scripts/setup_k8s.py
	$(KUBECTL) apply -f k8s/observability/namespace.yaml
	$(KUBECTL) apply -f k8s/observability/alertmanager/secret.yaml
	$(HELM) upgrade --install alertmanager prometheus-community/alertmanager \
		--version "$(AM_CHART_VERSION)" \
		--kube-context minikube \
		--namespace observability \
		--reset-values \
		-f "$(AM_VALUES_FILE)" \
		--rollback-on-failure --wait --timeout 5m

k8s-deploy-vmalert:
	$(KUBECTL) apply -f k8s/observability/namespace.yaml
	$(HELM) upgrade --install vmalert vm/victoria-metrics-alert \
		--version "$(VMA_CHART_VERSION)" \
		--kube-context minikube \
		--namespace observability \
		--reset-values \
		-f "$(VMA_VALUES_DIR)/values.yaml" \
		-f "$(VMA_VALUES_DIR)/rules.yaml" \
		--rollback-on-failure --wait --timeout 5m

k8s-update-image:
	docker build -t sre-f26-fileserver:local .
	minikube image load -p minikube sre-f26-fileserver:local --overwrite=true
	$(KUBECTL) -n fileserver rollout restart deployment/fileserver-deployment
	$(KUBECTL) -n fileserver rollout status deployment/fileserver-deployment --timeout=300s

k8s-deploy: k8s-bootstrap
	$(KUBECTL) apply -k k8s
	$(KUBECTL) -n fileserver rollout restart deployment/fileserver-deployment
	$(KUBECTL) -n fileserver rollout status deployment/fileserver-deployment --timeout=300s
	$(MAKE) -j1 k8s-deploy-victorialogs k8s-deploy-victoriametrics \
		k8s-deploy-vector k8s-deploy-grafana \
		k8s-deploy-alertmanager k8s-deploy-vmalert
