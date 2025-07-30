
Demo application to show integration with base infra: logging, metrics, traces

Start Grafana LGTM
```
docker run -p 3000:3000 -p 4317:4317 -p 4318:4318 --rm -ti grafana/otel-lgtm
```

Build application
```
go mod tidy
go build
```

Start application
```
./basement server
```

HTTP server started on default port 8080
Send query
```
curl "http://localhost:8080/livez"
```

Go to Grafana UI: "http://localhost:3000"
and check logs, metrics and traces from basement service


Test OTEL instrumentation with oats

```
go install github.com/grafana/oats@latest
```

```
oats oats.yaml
```