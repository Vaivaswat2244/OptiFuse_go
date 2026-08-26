# Deployer service — designed, not implemented

**Status: no Go code exists.** This directory holds a Dockerfile and this note. The RPC contract
is defined in `proto/services.proto` (`DeployerService`, `DeployRequest`, `DeployResponse`), the
compose block is commented out, and `deployer` is deliberately absent from `SERVICES` in the
Makefile so `make build` does not walk into a missing `cmd/` directory.

## What it is for

Today OptiFuse ends at a recommendation: *"merge `upload`, `resize` and `watermark`; that is 48%
cheaper."* Acting on it means the user goes back to their terminal, edits `serverless.yml` by
hand, and redeploys. The deployer closes that loop — apply the optimized configuration from the
platform itself.

That turns OptiFuse from an advisory tool into an observation *and* deployment management tool:
connect a repo, see what it costs, see what it could cost, and apply the change, without leaving
the product.

## How access would work

The customer already runs a CloudFormation stack that creates a **read-only** cross-account role
(`xray:*`, `logs:*`) for the enricher. The deployment flow extends that same onboarding with a
**second, deployment-scoped role** — so a customer who only wants analysis is never asked for
write access, and granting deploy rights is an explicit, separate decision.

```
gateway ──► deployer ──sts:AssumeRole──► Optifuse-User-Deploy-Role (customer account)
                                          │
                                          └─► lambda / cloudformation / s3 / iam:PassRole
```

Keeping the two roles separate matters: read-only analysis is an easy sell, and bundling write
permissions into it would make the whole product harder to adopt.

## Open problems to solve before implementing

These are not details — each one changes the shape of the service.

**1. `DeployRequest` carries no source code.** It has `original_yaml_content` and the chosen
plan, but `serverless deploy` needs the handlers. Either the deployer clones the repo (it already
has a GitHub token available via the gateway), or the output becomes a pull request against the
user's repo rather than a direct deploy. The PR route needs no AWS write access at all, which may
make the second IAM role unnecessary — worth deciding before building either.

**2. A merged `serverless.yml` alone does not fuse anything.** The saving comes from `upload`
calling `resize` **in-process** instead of through `InvokeCommand`. Deploying a configuration
that merges the memory allocation but leaves the handlers invoking each other over the network
pays the same data-transfer cost the optimizer assumed was eliminated — the user would see no
saving and reasonably conclude the tool does not work.

So something must generate a dispatcher: an entry point per group that imports its member
handlers and routes an internal call to a local function. That likely requires a documented
convention for what a handler must export, and it is language-specific — Node first, since both
reference applications are Node.

**3. Rollback.** Applying a fusion changes the deployed topology. There is no story yet for
reverting to the previous configuration if the fused version misbehaves.
