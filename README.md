# OptiFuse

**Serverless function-fusion optimizer.** Point it at a GitHub repo containing a
`serverless.yml`, and it tells you which of your Lambda functions to merge into a single
deployment to spend less money — and by how much.

---

## The idea

A serverless application built as a chain of small Lambdas pays for every hop between them:
once in **request charges** (the platform bills each function it invokes, $0.20 per million)
and once in **latency** (a cross-function invoke, roughly 10–20 ms, plus a cold start if the
callee has none warm). Merge two functions into one deployment and the call between them
becomes an in-process function call: no request charge, no hop.

Fusion is not free, though. A Lambda has one memory setting, so a fused function is sized for
its heaviest member and every lighter member runs at that setting for its whole duration.
Fold a 128 MB handler in with a 1024 MB one and the light path bills at 8× what it did. Which
side wins depends on the durations and memory tiers involved, which is why the question is a
**graph partitioning problem** with a trade-off rather than an answer:

> Partition the call graph into groups. For each partition, cost is request charges plus
> execution at group memory; latency is the critical path plus a hop per cut edge on it.

OptiFuse builds that graph from your `serverless.yml`, fills in real durations and invocation
rates from your CloudWatch, runs six partitioning algorithms, and shows you the **Pareto
frontier**: every partition that nothing else beats on both cost and latency. When one option
dominates everything it says so. When it does not, it shows you the cheapest, the fastest,
and the gap between them, and leaves the choice where it belongs.

```
       upload ──► resize ──► watermark ──┐
          │                              ├─► store
          └─────► filter ──► optimize ───┘

  Measured (image-processing test app, per million invocations):

    no fusion                 $3.50   297 ms
    MinWCut / MtxILP          $2.98   297 ms   cheapest: {filter, optimize, store} fused
    GreedyTP                  $3.08   287 ms
    Singleton (all in one)    $3.23   267 ms   fastest, pays to run upload at 512 MB
```

An earlier version of this model charged data transfer on every cut edge and reported savings
of up to 95%. Same-region Lambda-to-Lambda invocation is not billed as data transfer. The
story of finding that out is [here](https://medium.com/@vaivaswat2244/i-built-a-serverless-function-fusion-optimizer-then-i-changed-what-it-optimizes-e4daedbfe05b).

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
| `Singleton` | baseline | Everything in one group, sized to the heaviest member. Usually the fastest option and rarely the cheapest. |
| `MinWCut` | heuristic | Greedy merge along edges, accepting a merge only when the request saving exceeds the re-tiering cost. |
| `GreedyTP` | heuristic | Cut the critical path to satisfy latency, BFS-assign the rest, then greedily merge under the same gate. |
| `CostlessCSP` | heuristic | Label-setting over (cost, latency) states. |
| `MtxILP` | **exact** | Mixed-integer program solved by `glpsol`: request charge on cut edges plus execution with group memory linearised as a max. The cheapest feasible partition under the latency cap. |

`MtxILP` writes a CPLEX LP file to a temp path and invokes GLPK with a 60-second limit. If
`glpsol` is missing the algorithm returns a descriptive error instead of failing the run; the
other five still produce results.

Every result is then placed on the Pareto frontier (`algo/frontier.go`): a result is
*dominated* if another is no worse on both cost and latency and strictly better on one. The
response carries the full set, the dominance relation, the cheapest, the fastest, and whether
one option is unambiguously best.

### Cost model

Per application invocation, summed over fused groups:

```
request:     $0.0000002 × (invocation rate of each group's entry function)
execution:   $0.00001667 × (max member memory in GB) × Σ(member runtime in seconds)
transfer:    $0 by default (same-region Lambda-to-Lambda is not billed); configurable
latency:     Σ(runtime of critical path) + networkHopMS × (cut edges on that path)
```

Invocation rates come from CloudWatch invocation counts relative to the entry function, so a
fan-out target invoked twice per request saves two request charges when fused. Group memory is
the **largest** member, which is how Lambda actually provisions a single function; the
difference between that and each member's own memory, over the member's runtime, is the
re-tiering penalty that makes some fusions unprofitable.

Feasible means: no member exceeds `maxMemoryMB` **and** latency ≤ `maxLatencyMS`.

Known gaps, all tracked in `algo/optimizer.go`:

- **Cold starts are measured but not modelled.** The enricher reports average and p99 init
  duration and the cold-start rate per function (on the test apps, ~366 ms init against a
  ~133 ms warm critical path). The latency figure does not yet include them.
- **Provisioned concurrency** is not a candidate. It is the competing lever for tail latency.
- The pairwise profitability gate in the heuristics cannot see a merge that is unprofitable in
  isolation but pays off inside a larger group. `MtxILP` can.

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
      maxMemoryMB: 1024           # no single function may exceed this
      maxLatencyMS: 700           # budget for the critical path
      networkHopMS: 10            # latency added per cut edge on that path
      # dataTransferUSDPerGiB: 0  # only if your topology crosses a billed boundary
```

- **Edges define the graph; their byte counts no longer drive cost.** Same-region
  Lambda-to-Lambda invocation is not billed as data transfer, so `dataTransferUSDPerGiB`
  defaults to zero. Set it if calls cross regions or leave AWS.
- **`criticalPath` is required** by GreedyTP and CostlessCSP — they error without it, and the
  latency constraint goes unenforced.
- **`functions.avgDurationMs` matters more than it looks.** The only other runtime signal is
  `timeout`, in whole seconds, defaulting to 30 — roughly 100× a typical Lambda duration. Left
  at the default, the re-tiering penalty swamps the request saving and nothing fuses. These
  values land on the same field the enricher writes, so real CloudWatch data supersedes them
  per function. The response says which was used (`telemetry: cloudwatch | estimates`), and
  the UI labels results accordingly.
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

**Logging.** `shared/logger` — `log/slog`, JSON when `LOG_FORMAT=json`, level from `LOG_LEVEL`
(default `info`), every line tagged with `service`. The optimizer logs one `algorithm completed`
line per algorithm with cost, latency, feasibility and wall-clock time, which diagnoses most bad
recommendations from logs alone.

**Metrics.** Every service serves Prometheus text on `:9090/metrics` (override with
`METRICS_PORT`), on a separate listener from application traffic so it can be scraped inside the
cluster without being publicly reachable.

`shared/metrics` provides the transport-level RED metrics — `grpc_server_requests_total`,
`grpc_server_request_duration_seconds`, and the client-side equivalents on the gateway. The
client and server histograms are both worth having: a gap between them is a networking problem
rather than a slow handler.

The domain metrics are declared in the service that emits them, so a binary only exposes series
it can populate:

| Metric | Service | Why |
|---|---|---|
| `optifuse_algorithm_duration_seconds{algorithm}` | optimizer | MtxILP is exponential under a 60s deadline; the rest finish in microseconds |
| `optifuse_algorithm_failures_total{algorithm,reason}` | optimizer | MtxILP failed on every run for months unnoticed — a failing algorithm looks the same as a losing one |
| `optifuse_recommendation_total{algorithm}` | optimizer | Which algorithm actually wins in production |
| `optifuse_recommendation_savings_ratio` | optimizer | Saving vs NoFusion. Clustered near zero means the product isn't earning its keep |
| `optifuse_enrichment_functions_total{result}` | enricher | `missing` means a yml estimate silently stood in for real telemetry |
| `optifuse_enrichment_requests_total{result}` | enricher | Classified AWS failures — each label is a different fix |
| `optifuse_github_api_duration_seconds` | gateway | The dominant term in end-to-end latency |

Label values are bounded by design. Algorithm labels are stable slugs (`mtx_ilp`, not the
display string `MtxILP (Optimal)`) so retitling a UI column can't break a dashboard; error
reasons are classified rather than passed through, since raw AWS messages and solver output
embed request IDs and temp paths that would mint a new time series per request.

**Health checks** on every container: gRPC services register `grpc.health.v1.Health` and report
`NOT_SERVING` while draining; the gateway splits `/healthz` (liveness) from `/readyz`
(readiness). Postgres uses `pg_isready`.

**Graceful shutdown** everywhere — SIGTERM drains for 2s while endpoint removal propagates, then
stops within 25s, under Kubernetes' default 30s grace period.

**Correlation IDs.** Every log line for one user request carries the same `request_id`, across
all four services. The gateway honours an inbound `X-Request-ID` or generates one, echoes it in
the response header — so an ID copied from a browser network tab can be pasted straight into a
log query — and `shared/reqid` forwards it through gRPC metadata. A service called directly
generates its own rather than logging untagged.

**Timeouts.** Each hop of the simulation pipeline has its own budget (parse 10s, enrich 45s,
optimize 90s) beneath a 120s overall cap, all derived from the request context so a client
disconnect cancels everything. The optimize budget deliberately exceeds MtxILP's own 60s solver
deadline; a single shared 30s budget used to cancel the solver before it could report.

Still missing:

- No distributed tracing. `request_id` groups the lines but does not give you a span tree or
  per-hop timing without arithmetic. `shared/reqid` is the seam OpenTelemetry would slot into —
  the propagation points are already correct, so adopting it is a swap rather than a rewrite.
- No retries or circuit-breaking on gRPC calls.
- The compose health checks are still `nc -z`, which only proves the port is bound. Kubernetes
  will use the gRPC health service natively.

---

## Deployment

OptiFuse runs at [optifuse.vaivaswat.me](https://optifuse.vaivaswat.me) (frontend on Vercel) and
[api.optifuse.vaivaswat.me](https://api.optifuse.vaivaswat.me/readyz) (backend on AKS). Everything
about the backend deployment is in this repository:

```
infra/terraform/aks/        resource group + AKS cluster (one B2s node, OIDC issuer enabled)
infra/terraform/platform/   DNS zone, ingress-nginx, cert-manager, ArgoCD
deploy/base/                Kustomize base: namespace, ConfigMap, Postgres, 4 services, Ingress
deploy/overlays/dev/        image tags, pinned to a commit SHA
deploy/cluster/             Let's Encrypt ClusterIssuers
deploy/argocd/              the ArgoCD Application that watches deploy/overlays/dev
.github/workflows/ci.yml    test → build → push to GHCR → commit the new tag to the overlay
```

The loop: a push to `main` that touches code builds four images tagged with the commit SHA and
commits that tag into `deploy/overlays/dev`. ArgoCD, inside the cluster, applies what Git says.
CI holds no cluster credential; the cluster pulls.

Hand-created and documented rather than committed: three Kubernetes Secrets (Postgres password,
gateway OAuth + database URL, enricher AWS keys) and four NS records at the registrar delegating
`optifuse.vaivaswat.me` to Azure DNS.

`docs/DEPLOYMENT_GUIDE.md` walks through every piece, why it is shaped the way it is, and what
breaks otherwise.

The `docker-compose.yml` is the **development** topology: ports published to the host,
hardcoded Postgres credentials, no resource limits. It is not the deployment.

---

## Status

- [x] Link a GitHub repo and read its `serverless.yml`
- [x] Build a call graph from a `custom.optifuse` block
- [x] Six partitioning algorithms, ranked by cost under memory and latency constraints
- [x] CloudWatch telemetry through a customer-supplied cross-account IAM role
- [x] Show the Pareto frontier: every option nothing else beats, with cheapest, fastest and
      the gap between them; a single recommendation only when one option dominates
- [x] Deployed: Terraform-built AKS, Kustomize manifests, Let's Encrypt TLS, ArgoCD, CI to GHCR
- [ ] Cold-start term in the latency model (init duration is already measured per function)
- [ ] Provisioned concurrency as a competing candidate
- [ ] Before/after measurement: deploy a recommended fusion and compare real CloudWatch numbers
- [ ] Workload identity for the enricher (no stored AWS keys)
- [ ] Prometheus + Grafana dashboard on the services' existing `/metrics`
- [ ] Derive the call graph from X-Ray traces instead of a hand-written block *(needs the
      customer's handlers instrumented with `aws-xray-sdk-core`; raw `InvokeCommand` does not
      propagate the trace header, so every Lambda lands in its own trace)*
- [ ] Persist simulation results — there is no history, so nothing can show that a fusion
      actually saved money after the fact
- [ ] Generate and deploy the fused artifact (`deployer` is proto + Dockerfile only)
- [ ] Support formats other than the Serverless Framework (SAM, CDK, Terraform)
