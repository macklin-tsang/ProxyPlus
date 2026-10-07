# ProxyPlus

A static web server, a caching HTTP proxy, and a framed-multiplexing demo, written in Go
using only the standard library. It replaces an earlier Python version, which is still
in the git history at commit `f1cdbec`.

| Program | Listens on | Metrics |
|---|---|---|
| `cmd/webserver` | `127.0.0.1:8080` (serves the current directory) | `127.0.0.1:9101/metrics` |
| `cmd/proxy` | `127.0.0.1:8888` (HTTP), `127.0.0.1:8889` (framed) | `127.0.0.1:9102/metrics` |
| `cmd/framedclient` | connects to `127.0.0.1:8889` | none |

## Build, test, run (from the repo root)

    go build -o bin/ ./...
    go vet ./... && go test -race ./...
    .\bin\webserver.exe          # terminal 1
    .\bin\proxy.exe              # terminal 2

## Demo: no head-of-line blocking

    .\bin\framedclient.exe http://127.0.0.1:8080/frame_data.bin http://127.0.0.1:8080/test.html
    stream 3        521 B  done at 0.007s
    stream 1    2097384 B  done at 2.096s

The 2 MiB object and the small page share one connection. Frames are sent round robin at
1 MiB/s, so the small page finishes first. Through the plain proxy:

    curl.exe -i -x http://127.0.0.1:8888 http://127.0.0.1:8080/test.html    # X-Cache: MISS, then HIT

Frame layout (big-endian): 4-byte payload length, 4-byte stream id, 1-byte end flag, payload.

## Metrics and dashboard

Each server exposes Prometheus metrics (code in `metrics/`): request count, active
connections and bytes sent. The proxy also exposes cache hits and misses, bytes read from
origins, and an upstream latency histogram. To see them in Grafana (needs Docker):

    cd monitoring
    docker compose up -d       # then open http://localhost:3000 (dashboard "ProxyPlus")
    docker compose down

Prometheus reaches the servers through `host.docker.internal`. Generate traffic with the
commands above; the cache hit ratio and latency panels need proxy traffic.
