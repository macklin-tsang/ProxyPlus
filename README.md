# ProxyPlus

A static web server, a caching HTTP proxy, and a framed-multiplexing demo, written in Go
using only the standard library. Each Go function mirrors a function in the original Python
(`webserver.py`, `proxy.py`, `framed_client.py`), whose names appear in the code comments.

| Program | Listens on | Metrics |
|---|---|---|
| `cmd/webserver` | `0.0.0.0:8080` (serves the current directory) | `127.0.0.1:9101/metrics` |
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

Each program exposes Prometheus metrics (code in `metrics/`): request count, cache
hits and misses, active connections, bytes sent, bytes read from origins, and an upstream
latency histogram. To see them in Grafana (needs Docker):

    cd monitoring
    docker compose up -d       # then open http://localhost:3000 (dashboard "ProxyPlus")
    docker compose down

Prometheus reaches the servers through `host.docker.internal`. Generate traffic with the
commands above; the cache hit ratio and latency panels need proxy traffic.

## Differences from the Python version

Sibling-folder paths such as `/../ProxyPlus2/x` get 403, 400 says "Bad Request",
If-Modified-Since works with whole-second dates, bad ports return 502 instead of hanging,
and request heads and frames are size-capped.
