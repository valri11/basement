# Basement — Deployment Guide

OTEL reference app. Deploy this first to validate the observability pipeline (traces, metrics, logs) before migrating real services.

## Prerequisites

- K8s cluster running ([k8s-cluster guide](https://mywiki.golemresearch.net/homelab/k8s-cluster/))
- OTel Collector DaemonSet deployed ([k8s-infra guide](https://mywiki.golemresearch.net/homelab/k8s-infra/))
- Tempo, Prometheus, Loki, Grafana running in `monitoring` namespace
- `helm` CLI installed
- Image pushed to registry: `tg-tc:10443/basement:latest`

## Build and push image

```sh
# From project root
docker build -t tg-tc:10443/basement:latest .
docker push tg-tc:10443/basement:latest
```

Or let Gitea Actions handle it (see `.gitea/workflows/build.yaml`).

## Deploy

### Dev environment

```sh
helm install basement ./k8s/basement \
  -f k8s/basement/envs/dev/values.yaml \
  -n apps --create-namespace
```

### Upgrade after code change

```sh
helm upgrade basement ./k8s/basement \
  -f k8s/basement/envs/dev/values.yaml \
  -n apps
```

### Uninstall

```sh
helm uninstall basement -n apps
```

## Verify

### 1. Pod is running

```sh
kubectl -n apps get pods -l app.kubernetes.io/name=basement
# STATUS: Running
```

### 2. Health check

```sh
# Port-forward (if no ingress)
kubectl -n apps port-forward svc/test-basement 8080:8080 &

curl http://localhost:8080/livez
# {"status":"ok"}
```

### 3. Ingress (if enabled)

```sh
# Add DNS
echo "192.168.3.200 basement.home.lab" | sudo tee -a /etc/hosts

curl http://basement.home.lab/livez
# {"status":"ok"}
```

### 4. Validate observability pipeline

Open Grafana at `http://grafana.home.lab`:

**Traces:**
- Explore → Tempo → Service Name = `basement` → Run query
- `curl http://basement.home.lab/demo/trace`, then expect `GET /demo/trace` (SERVER) with child `demo.work`
- Probes (`/livez`, `/readyz`) are not traced

**Logs:**
- Explore → Loki → `{app="basement"}` → Run query
- Should see JSON structured logs with trace IDs

**Metrics:**
- Explore → Prometheus → `http_server_request_duration_seconds_count{service_name="basement"}` → Run query
- Errors by cause: `servicepack_http_server_problems_total{service_name="basement"}` (labels `http_route`, `http_response_status_code`, `problem_type`)

**Correlation:**
- Click a trace span → "Logs for this span" → should show matching log lines
- This confirms OTel Collector is routing all three signals correctly

## Troubleshooting

### No traces in Tempo

```sh
# Check OTel Collector is running on the node
kubectl -n monitoring get ds otel-collector

# Check collector logs for export errors
kubectl -n monitoring logs -l app=otel-collector --tail=50

# Verify basement is sending to collector
kubectl -n apps logs -l app.kubernetes.io/name=basement | grep "otel"
```

### Pod crash / OOMKilled

```sh
kubectl -n apps describe pod -l app.kubernetes.io/name=basement
# Check Events section for OOMKilled, then bump memory limits in values.yaml
```

### Config changes not picked up

The deployment has a `checksum/config` annotation — Helm automatically restarts pods when the ConfigMap changes. If you edited the ConfigMap manually:

```sh
kubectl -n apps rollout restart deployment test-basement
```

## Helm values reference

| Value | Default | Description |
|-------|---------|-------------|
| `replicaCount` | 1 | Number of pods |
| `image.repository` | tg-tc:10443/basement | Container registry |
| `image.tag` | latest | Image tag |
| `service.port` | 8080 | Service port |
| `ingress.enabled` | false | Enable Ingress resource |
| `ingress.host` | basement.home.lab | Ingress hostname |
| `resources.requests.memory` | 32Mi | Memory request |
| `resources.limits.memory` | 64Mi | Memory limit |
| `config.server.disableTelemetry` | false | Disable OTEL export |
| `config.server.logLevel` | info | Log level (stdout and OTLP) |
| `otel.endpoint` | `http://$(NODE_IP):4317` | OTLP gRPC endpoint URL |
| `otel.environment` | "" | `deployment.environment.name` |
