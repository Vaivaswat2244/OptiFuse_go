# OptiFuse

**Serverless function-fusion optimizer.** Point it at a GitHub repo containing a
`serverless.yml`, and it tells you which of your Lambda functions to merge into a single
deployment to spend less money — and by how much.

---

## The idea

A serverless application built as a chain of small Lambdas pays twice for every hop between
them: once in **data transfer** (the payload crosses the network) and once in **latency** (a
cold cross-function invoke, roughly 10–20 ms). Merge two functions into one deployment and
both costs disappear for that edge — the call becomes an in-process function call.

You cannot merge everything, though. A fused group is bounded by memory, and every hop you
*keep* adds latency to your critical path. So the question is a **graph partitioning problem**:

> Partition the call graph into groups such that total cost is minimised, subject to
> a per-group memory cap and an end-to-end latency budget.

OptiFuse builds that graph, runs six different partitioning algorithms over it, and ranks
their answers by total cost.

```
       upload ─5MiB─► resize ─2MiB─► watermark ─2MiB─┐
          │                                          ├─► store
          └────5MiB─► filter ─3MiB─► optimize ─1MiB──┘

  becomes  { upload, resize }  { watermark, store }  { filter, optimize }
           ─ 3 deployments instead of 6, ~55% cheaper
```

---

## Architecture

Four Go services over gRPC, one Postgres, one Next.js frontend. Each service is independently
deployable and does exactly one thing; the split is drawn along **credential boundaries** as
much as along responsibilities — only the enricher ever holds AWS keys, and only the gateway
ever holds user tokens.

```
                    ┌──────────────────┐
                    │  Next.js client  │
                    └────────┬─────────┘
                             │ REST/JSON  :8080
                    ┌────────▼─────────┐         ┌──────────────┐
                    │     gateway      │◄───────►│  Postgres    │
                    │  auth, orchestr. │         │ users,tokens │
                    └────────┬─────────┘         └──────────────┘
                             │ gRPC
        ┌────────────────────┼────────────────────┐
        │                    │                    │
┌───────▼────────┐  ┌────────▼───────┐  ┌─────────▼────────┐
│    parser      │  │    enricher    │  │    optimizer     │
│    :50051      │  │    :50052      │  │    :50053        │
│                │  │                │  │                  │
│ serverless.yml │  │  CloudWatch    │  │  6 algorithms    │
│    → Graph     │  │  → telemetry   │  │  → ranked plans  │
│                │  │                │  │                  │
│ no credentials │  │ AWS creds +    │  │ no credentials   │
│                │  │ AssumeRole     │  │ shells to glpsol │
└────────────────┘  └────────┬───────┘  └──────────────────┘
                             │ sts:AssumeRole
                    ┌────────▼─────────┐
                    │ customer AWS acct│
                    │ CloudWatch Logs  │
                    └──────────────────┘
```

| Service | Port | Stateful | Credentials | Role |
|---|---|---|---|---|
| `gateway` | 8080 | Postgres | GitHub OAuth, user tokens | REST API, auth, orchestration |
| `parser` | 50051 | no | none | `serverless.yml` → `Graph` |
| `enricher` | 50052 | no | AWS (assumes customer role) | CloudWatch → telemetry on `Graph` |
| `optimizer` | 50053 | no | none | `Graph` → six partitions, ranked |
| `deployer` | 50054 | — | — | **not implemented** — proto and Dockerfile only |

`parser` and `optimizer` are pure functions of their input: same bytes in, same graph out. That
makes them trivially horizontally scalable and trivially testable. `enricher` is the only
service that touches the network besides the gateway.

---

## Request lifecycle

There is essentially one interesting path through the system — `POST /api/simulate/live/`
(`services/gateway/internal/handlers/simulate.go`):

1. **Fetch.** Gateway pulls `serverless.yml` from the GitHub Contents API using the user's
   stored OAuth token. *Nothing is cloned; only the one file is read.*
2. **Parse** (gRPC → parser). YAML becomes a `Graph`: nodes with memory/timeout/runtime, edges
   with byte counts, plus constraints and the critical path. Returns the yml's `service:` and
   `provider.stage`, which the enricher needs.
3. **Enrich** (gRPC → enricher), *skipped if the user has configured no AWS role*. Assumes the
   customer's cross-account IAM role, runs a CloudWatch Logs Insights query, and overwrites
   `avg_duration_ms`, `avg_memory_used_mb` and `invocation_count` **per function, only where
   real data came back**. Failure here is non-fatal: it becomes a warning and the run continues
   on YAML-derived values.
4. **Optimize** (gRPC → optimizer). Runs all six algorithms, computes metrics for each, and
   picks the cheapest *feasible* one as `recommended`.
5. **Respond.** `{ results: { results: [...], recommended: {...} }, warnings: [...] }`.

The whole round trip is typically 300–700 ms, almost all of it the GitHub API call — the
algorithms themselves run in single-digit milliseconds on graphs of this size.

---

## Data contracts

Three proto files in `proto/`, all generated with `make proto`.

- **`graph.proto`** — the core domain. `FunctionNode` splits deliberately into *static* fields
  (from YAML, ids 1–9) and *telemetry* fields (from the enricher, ids 20–25). A zero telemetry
  value means "not enriched", and every algorithm must degrade gracefully when it sees one.
- **`optimizer.proto`** — `FusionGroup` (the actual answer: which function IDs to merge, plus
  aggregate memory/runtime/cost), `Metrics`, `AlgorithmResult`, `OptimizationPlan`.
- **`services.proto`** — the four service definitions and their request/response envelopes.

`FunctionNode` is intentionally cloud-agnostic (memory, timeout, handler, runtime), which is
what would make a non-AWS adapter possible later.

---

## The six algorithms

All live in `services/optimizer/internal/algo/`, each implementing a one-method `Optimizer`
interface, each in its own file.

| Algorithm | Kind | Approach |
|---|---|---|
| `NoFusion` | baseline | Every function separate. The number everything else is measured against. |
| `Singleton` | baseline | Everything in one group. Usually infeasible on memory — that's the point. |
| `MinWCut` | heuristic | Greedy merge by descending edge weight, subject to the memory cap. |
| `GreedyTP` | heuristic | Cut the critical path to satisfy latency, BFS-assign the rest, then greedily merge. |
| `CostlessCSP` | heuristic | Label-setting over the Pareto frontier of (cost, latency). |
| `MtxILP` | **exact** | Formulates an ILP and shells out to `glpsol`. Optimal, exponential worst case. |

`MtxILP` writes a CPLEX LP file to a temp path and invokes GLPK with a 60-second limit. If
`glpsol` is missing the algorithm returns a descriptive error instead of failing the run — the
other five still produce results.

### Cost model

```
execution:      $0.00001667 per GB-second
data transfer:  $0.01 per GiB on every cut edge
latency:        Σ(runtime of critical path) + networkHopMS × (cut edges on that path)
```

Feasible means: no group exceeds `maxMemoryMB` **and** latency ≤ `maxLatencyMS`.

⚠️ **Read `services/optimizer/internal/algo/optimizer.go` before trusting a number.** The
package doc there records three deliberate simplifications — most importantly that a fused
group is charged the *sum* of its members' memory rather than one allocation, which
structurally penalises fusion, and that the solvers minimise cut-transfer while results are
ranked on total cost.

---

## What OptiFuse needs from a serverless.yml

**A `functions:` block alone is not enough.** OptiFuse needs the *call graph*, and nothing in a
stock `serverless.yml` describes which function invokes which. Without it the graph has no
edges and every algorithm degenerates — NoFusion (nothing to merge) or an outright error (no
critical path). This is the single most common reason a run comes back with no fusion.

Supply it under `custom.optifuse`, which the Serverless Framework itself ignores:

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

- **Edge bytes drive everything.** Data transfer is the cost fusion eliminates. Rough estimates
  are fine; orders of magnitude are what matter.
- **`criticalPath` is required** by GreedyTP and CostlessCSP — they error without it, and the
  latency constraint goes unenforced.
- **`functions.avgDurationMs` matters more than it looks.** The only other runtime signal is
  `timeout`, in whole seconds, defaulting to 30 — roughly 100× a typical Lambda duration. Left
  at the default, execution cost swamps transfer cost and nothing fuses. These values land on
  the same field the enricher writes, so real CloudWatch data supersedes them per function.
- **Telemetry does not supply the graph.** The Logs Insights query over `@type = "REPORT"`
  records returns duration, memory and invocation count — no caller→callee information. Traffic
  improves the numbers; it does not create edges.

`services/parser/internal/parser_test.go` carries a complete worked example.

---

## AWS access

OptiFuse never asks for AWS keys. Instead the user deploys a small CloudFormation stack in
their own account which creates a **read-only cross-account role**, and pastes the resulting
role ARN into their settings.

```
enricher (OptiFuse account)
   │  sts:AssumeRole  +  ExternalId
   ▼
Optifuse-User-Access-Role (customer account)
   │  read-only
   ▼
xray:GetTraceSummaries · xray:BatchGetTraces
logs:StartQuery · logs:GetQueryResults · logs:DescribeLogGroups
```

Two things the enricher needs and that are easy to get wrong:

- **Base credentials.** Assuming a role is itself an AWS call, so the enricher must already be
  authenticated as the account named in the customer's trust policy. Without any credentials
  the SDK falls through to EC2 instance metadata, which doesn't exist in a container, and
  blocks ~5 s before failing.
- **Log group names.** These are `/aws/lambda/{service}-{stage}-{function}`, built from the
  yml's `service:` and `provider.stage` — **not** the GitHub repo name, which is frequently
  different. Get this wrong and every query silently matches nothing.

The external ID is generated per profile (`uuid_generate_v4()`), so **recreating the database
volume invalidates any existing CloudFormation stack** — the trust condition will no longer
match and you get an opaque 403.

---

## Running locally

Requires Docker, and Go 1.26+ / `protoc` only if you're regenerating code.

```sh
cp .env.example .env     # then fill it in
make docker-up           # builds and starts everything
```

`.env`:

| Variable | Used by | Notes |
|---|---|---|
| `DATABASE_URL` | gateway | Postgres DSN |
| `GITHUB_CLIENT_ID` / `_SECRET` | gateway | OAuth app credentials |
| `CLIENT_ORIGIN_URL` | gateway | CORS allow-list for the frontend |
| `AWS_ACCESS_KEY_ID` / `_SECRET_ACCESS_KEY` | enricher | OptiFuse's *own* account, used to assume customer roles |
| `AWS_REGION` | enricher | Must match where the customer's Lambdas run |
| `LOG_FORMAT` | all | `json` for structured output; text otherwise |

Postgres bootstraps its schema from `services/gateway/internal/db/schema.sql` on first run
only — three tables: `users`, `profiles`, `tokens`.

```sh
make test          # all tests
make test-parser   # one service
make proto         # regenerate protobuf (needs protoc-gen-go and -grpc on PATH)
make build         # binaries into bin/
make docker-down
```

> `MtxILP` needs `glpsol`, which is installed in the optimizer image but probably not on your
> host. Its round-trip test skips when absent — run the suite in a container to exercise it.

### HTTP API

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `GET` | `/health` | — | Liveness |
| `POST` | `/api/auth/github/` | — | Exchange a GitHub OAuth code for an API token |
| `GET` | `/api/repositories/` | token | List the user's repos |
| `GET` | `/api/repositories/:owner/:repo/file/` | token | Fetch a file (used for `serverless.yml`) |
| `GET`/`POST` | `/api/profile/settings/` | token | Read / set the AWS role ARN |
| `POST` | `/api/simulate/live/` | token | **Run the full pipeline** |

Auth is a DRF-style opaque token in an `Authorization: Token <key>` header, carried over from
the original Django implementation.

---

## Observability

What exists today:

- **Structured logging** everywhere via `shared/logger` — `log/slog`, JSON when `LOG_FORMAT=json`,
  every line tagged with `service`. The optimizer logs one `algorithm completed` line per
  algorithm with cost, latency, feasibility, group count and wall-clock time, which is enough to
  diagnose most bad recommendations from logs alone.
- **Health checks** on every container: TCP probes for the gRPC services, `GET /health` for the
  gateway, `pg_isready` for Postgres.
- **A panic-recovery interceptor** on the optimizer, so a malformed user-supplied topology
  returns a gRPC error instead of taking down the process.

What's missing, and worth building alongside a real deployment:

- No metrics. There is no `/metrics` endpoint and no Prometheus client — no request rate,
  latency histogram, or error rate for any service.
- No distributed tracing. A slow `/api/simulate/live/` cannot be attributed across the four
  services without reading logs by timestamp.
- No correlation ID. Nothing ties the gateway's log line to the parser and optimizer lines from
  the same request, which makes concurrent requests genuinely hard to untangle.
- gRPC calls have a single 30-second timeout for the whole downstream chain, no per-service
  budget, and no retry or circuit-breaking.
- Log level is hardcoded to `Debug` in `shared/logger`, which is noisy for production.

---

## Deployment notes

The `docker-compose.yml` here is a **development** topology and should not be lifted as-is:

- Every service publishes its port to the host, including Postgres and the internal gRPC
  services. In a real deployment only the gateway should be reachable.
- Postgres credentials are hardcoded in the compose file.
- Postgres has no volume — **data is lost on `docker compose down`**, which also regenerates
  every profile's AWS external ID and breaks existing customer CloudFormation stacks.
- There are no resource limits, and no readiness/liveness distinction.

---

## Status

- [x] Link a GitHub repo and read its `serverless.yml`
- [x] Build a call graph from a `custom.optifuse` block
- [x] Six partitioning algorithms, ranked by cost under memory and latency constraints
- [x] CloudWatch telemetry through a customer-supplied cross-account IAM role
- [x] Show the recommended grouping — which functions to merge, with per-group memory,
      runtime and cost
- [ ] Derive the call graph from X-Ray traces instead of a hand-written block *(needs the
      customer's handlers instrumented with `aws-xray-sdk-core`; raw `InvokeCommand` does not
      propagate the trace header, so every Lambda lands in its own trace)*
- [ ] Persist simulation results — there is no history, so nothing can show that a fusion
      actually saved money after the fact
- [ ] Generate and deploy the fused artifact (`deployer` is proto + Dockerfile only)
- [ ] Support formats other than the Serverless Framework (SAM, CDK, Terraform)
