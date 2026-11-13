#!/usr/bin/env bash

set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."


function err_not_found()
{
    echo "failed to locate $1" >&2
}


# Check for the needed components
for component in make minikube helm docker; do
    if ! command -v "$component" >/dev/null 2>&1; then
        err_not_found "$component"
        exit 1
    fi
done

if ! docker info >/dev/null 2>&1; then
    echo "Docker daemon is unavailable or permission is denied" >&2
    exit 1
fi


# Start the cluster
minikube start --driver=docker --ports=127.0.0.1:80:80

# Add an ingress
minikube addons enable ingress

# Allow snippet annotations
minikube kubectl -- -n ingress-nginx patch configmap ingress-nginx-controller \
    --type=merge \
    -p '{
      "data": {
        "allow-snippet-annotations": "true",
        "annotations-risk-level": "Critical"
      }
    }'

# Restart the controller with the updated configuration
minikube kubectl -- -n ingress-nginx rollout restart \
    deployment/ingress-nginx-controller

minikube kubectl -- -n ingress-nginx rollout status \
    deployment/ingress-nginx-controller \
    --timeout=300s

docker build -t sre-f26-fileserver:local .
minikube image load sre-f26-fileserver:local


# Initialize and deploy
make k8s-bootstrap
make k8s-deploy
