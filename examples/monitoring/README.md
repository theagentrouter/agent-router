This `monitoring.yaml` file is a Kubernetes manifest file that deploys a Prometheus server that scrapes metrics from Envoy Gateway pods where AI Gateway filter is enabled.

## Grafana dashboard

`grafana-dashboard.json` is an importable Grafana dashboard covering the `gen_ai_*`
metrics exposed by the AI Gateway (see [Observability docs](https://aigateway.envoyproxy.io/docs/capabilities/observability/)
for the full metric reference). It includes:

- Total requests and total tokens (input + output) as quick-glance stats
- P95 time-to-first-token (TTFT) and P95 request duration as quick-glance stats
- Token usage broken down by `gen_ai_token_type` (input / output / cached_input /
  cache_creation_input / reasoning) — useful for spotting prompt-caching
  effectiveness and reasoning-token overhead
- Request duration percentiles (P50 / P95 / P99) over time
- TTFT vs. time-per-output-token (inter-token latency) over time — TTFT measures
  perceived startup latency, time-per-output-token measures streaming pace
- A table of requests grouped by `gen_ai_original_model` vs. `gen_ai_response_model`
  — a mismatch between these two means the upstream provider silently swapped
  the model it actually served
- Request throughput (QPS) over time — the stat panels above only show
  cumulative totals, this shows the trend
- Token consumption rate (tokens/sec by type) over time — a proxy for cost
  burn rate, since spend scales with tokens/sec
- Error rate — share of requests where `gen_ai.error.type` was set. Note the
  gateway currently only records a generic error marker for GenAI requests
  (no granular reason code), unlike its richer MCP error taxonomy
- Success rate by model — useful for spotting one model degrading while
  others stay healthy. Grouped by `gen_ai_original_model` only, so if the
  same model name is served through more than one `AIServiceBackend`, this
  panel won't distinguish between them

### Importing

1. In Grafana, go to **Dashboards → New → Import**.
2. Upload `grafana-dashboard.json` (or paste its contents).
3. When prompted, select the Prometheus data source that scrapes your AI Gateway's
   `/metrics` endpoint (see `monitoring.yaml` for a Kubernetes-based scrape config,
   or point directly at `aigw run`'s admin port, e.g. `localhost:1064/metrics`,
   for local testing).

### Local testing without Kubernetes

You can validate the dashboard against a local `aigw run` instance without any
Kubernetes setup:

```bash
# 1. Run the gateway locally (see examples/aigw/ for config examples).
OPENAI_API_KEY=... aigw run --admin-port=1064

# 2. Point a local Prometheus at the admin metrics endpoint.
cat << 'EOF' > prometheus.yml
global:
  scrape_interval: 3s
scrape_configs:
  - job_name: 'envoy-ai-gateway'
    static_configs:
      - targets: ['localhost:1064']
EOF
docker run -d --network=host -v "$PWD/prometheus.yml:/etc/prometheus/prometheus.yml" \
  prom/prometheus --config.file=/etc/prometheus/prometheus.yml --web.listen-address=:9091

# 3. Run Grafana and import grafana-dashboard.json, pointing its Prometheus
#    data source at http://localhost:9091.
docker run -d --network=host grafana/grafana
```

Send a few chat completion requests through the gateway to populate the panels.
