# OptiFuse_go

Serverless function-fusion optimizer. Reads a `serverless.yml` from a linked GitHub
repo, builds a call graph, enriches it with CloudWatch telemetry, and runs six
partitioning algorithms to find a cheaper deployment grouping.

```
gateway ──gRPC──► parser    (serverless.yml → Graph)
   │      ──gRPC──► enricher  (CloudWatch → telemetry on Graph)
   │      ──gRPC──► optimizer (Graph → 6 algorithms → recommendation)
   └── REST ──► Next.js frontend
```

`make docker-up` brings the stack up; only the gateway is exposed, on `:8080`.

## Features

- [x] Link a GitHub repo and read its `serverless.yml`
- [x] Build a call graph from a `custom.optifuse` block (see below)
- [x] Six partitioning algorithms, ranked by total cost under memory and latency constraints
- [x] CloudWatch telemetry via a customer-supplied cross-account IAM role
- [ ] **Show the recommended grouping in the UI.** Today the frontend reports only
      *how many* groups an algorithm produced — a count, not an answer. The user is
      told "MinWCut, 3 groups, 48% cheaper" without ever learning *which functions
      to merge*, which is the actual deliverable. The data is already there: each
      result carries `groups[].function_ids` plus `total_memory_mb`,
      `total_runtime_ms` and `execution_cost_usd` (see `FusionGroup` in
      `proto/optimizer.proto`), and the gateway passes it through untouched. Only the
      rendering is missing. Note the frontend currently types this as `string[][]`,
      which does not match the wire format.
- [ ] Derive the call graph from X-Ray traces instead of a hand-written block
- [ ] Generate the fused deployment artifact (the `deployer` service is stubbed out)

## What OptiFuse needs from a serverless.yml

**A `functions:` block alone is not enough.** OptiFuse needs the *call graph*, and
nothing in a stock `serverless.yml` describes which function invokes which. Without
it the graph has no edges and every algorithm degenerates — NoFusion (nothing to
merge) or an outright error (no critical path). This is the single most common
reason a run comes back with no fusion.

Supply it under `custom.optifuse`, which the Serverless Framework ignores:

```yaml
custom:
  optifuse:
    topology:                     # who calls whom, and bytes on each edge
      upload:
        children:
          resize: 5242880         # 5 MiB
      resize:
        children:
          watermark: 2097152

    criticalPath:                 # latency-sensitive chain, in order
      - upload
      - resize
      - watermark

    functions:                    # runtime estimates, until you have telemetry
      upload:    { avgDurationMs: 100 }
      resize:    { avgDurationMs: 300 }
      watermark: { avgDurationMs: 150 }

    constraints:
      maxMemoryMB: 1024           # cap per fused group (group memory = SUM of members)
      maxLatencyMS: 700           # budget for the critical path
      networkHopMS: 10            # latency added per cut edge on that path
```

Notes:

- **Edge bytes drive everything.** Data transfer is the cost fusion eliminates.
  Rough estimates are fine; orders of magnitude are what matter.
- **`criticalPath` is required** by GreedyTP and CostlessCSP — they return an error
  without it, and the latency constraint is unenforced.
- **`functions.avgDurationMs` matters more than it looks.** The only other runtime
  signal is `timeout`, in whole seconds, defaulting to 30 — roughly 100x a typical
  Lambda duration. Left at the default, execution cost swamps transfer cost and
  nothing fuses. These values land on the same field the enricher writes, so real
  CloudWatch data supersedes them per-function once the app has traffic.
- **Telemetry does not supply the graph.** The enricher's Logs Insights query over
  `@type = "REPORT"` records returns duration, memory and invocation count — no
  caller→callee information. Traffic improves the numbers; it does not create edges.
  Deriving topology from X-Ray traces is the intended next step, and needs the
  handlers instrumented with `aws-xray-sdk-core` so the trace header propagates.

`services/parser/internal/parser_test.go` carries a complete worked example.

## Known model limitations

The solvers minimize data-transfer cost only, while results are ranked on total
cost including execution; and the cost model charges a fused group the *sum* of its
members' memory, which structurally penalizes fusion. See the package doc in
`services/optimizer/internal/algo/optimizer.go` for the full list — worth reading
before trusting a number.

## Development

```sh
make test        # all tests
make proto       # regenerate protobuf (needs protoc + protoc-gen-go[-grpc] on PATH)
make build       # binaries into bin/
make docker-up   # full stack
```
