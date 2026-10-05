# How OptiFuse is deployed, and why each piece is shaped the way it is

This is the document to read with a terminal open. Every section has a concept, the reason the
concept matters here, the thing that breaks if you ignore it, and a command to run so you see
it rather than take it on trust. Exercises are marked **Try it**. Do them. Reading about
Kubernetes is roughly as useful as reading about swimming.

Set this once per terminal and everything below works:

```sh
export KUBECONFIG=~/.kube/optifuse
export PATH=$HOME/.local/bin:$PATH     # terraform lives here
```

---

## 0. The one idea underneath all of it

Almost every tool in this stack is a **reconciler**: you declare the state you want, something
compares it with the state that exists, and it makes the difference go away.

| Tool | You declare | It reconciles | When |
|---|---|---|---|
| Terraform | `.tf` files | cloud resources | when you run `apply` |
| Kubernetes | YAML objects | pods, disks, load balancers | continuously, forever |
| ArgoCD | a Git path | Kubernetes objects | continuously, from Git |
| cert-manager | a Certificate | a signed cert in a Secret | continuously, renews before expiry |

Once you see this, the questions become uniform: *what is the desired state, who holds it,
what is comparing, and how often.* Debugging is finding which of those four broke.

---

## 1. The shape of the system

```
 browser ──► optifuse.vaivaswat.me          Vercel (Next.js)
                 │ fetch, Authorization: Token …
                 ▼
          api.optifuse.vaivaswat.me         Azure DNS A record ──► 172.198.227.215
                 │
          Azure Standard Load Balancer      created by Kubernetes from a Service
                 │
          ingress-nginx pod                 terminates TLS (Let's Encrypt), routes by Host
                 │
          gateway Service ──► gateway pod   Go, Gin, REST
                 │ gRPC                 │
      ┌──────────┼──────────┐           └──► postgres-0 (StatefulSet, 4 GiB Azure disk)
      ▼          ▼          ▼
   parser    enricher    optimizer
                │              │
                ▼              └──► glpsol (in the image)
          STS AssumeRole ──► customer role ──► CloudWatch Logs Insights
```

Everything from the load balancer down lives in one AKS cluster, on one `Standard_B2s` node
(2 vCPU, 4 GiB) in Azure's Hyderabad region. Everything is described in this repository except
three Secrets and four DNS records at the registrar.

**Try it:** `kubectl get all -n optifuse` and match each line to a box above.

---

## 2. Images

A container image is the unit of deployment. Each service has a `Dockerfile` with the same
shape; read `services/gateway/Dockerfile` and note:

- **Two stages.** A `golang` builder compiles; an `alpine` runtime gets only the binary. The
  result is 33 to 54 MB. The Go toolchain never ships.
- **`CGO_ENABLED=0`** produces a static binary, so the runtime image needs no libc. This is why
  Alpine works and why the image could be `scratch` if the optimizer didn't need `glpsol`.
- **`-ldflags "-X main.version=… -X main.commit=…"`** stamps the commit into the binary. The
  gateway logs it at startup, so "which build is this pod running" always has an exact answer.
- **`adduser -D -u 10001 optifuse` + `USER optifuse`.** The process never runs as root. The
  cluster's namespace enforces this (section 7), so an image that forgot would be refused.

Images are tagged with the **short commit SHA**, never `latest`. `latest` is a moving name:
two pods can run different code under it, rollback is impossible because the old image has no
name, and "what is deployed" becomes unanswerable. A SHA is an exact commit.

They live in GitHub's registry, `ghcr.io/vaivaswat2244/optifuse-<service>:<sha>`, public. A
private image would need an `imagePullSecret` in every namespace that pulls it. The source is
public anyway.

**Try it:** `make images TAG=test` builds all four locally. `docker history ghcr.io/vaivaswat2244/optifuse-gateway:test` shows every layer and its size.

---

## 3. Terraform

### State, and why it is dangerous

Terraform compares three things: your files (desired), its **state file** (what it believes it
created), and the real world (refreshed on every plan). The state file is what lets it know
that "the cluster block" means *that* cluster and not some other one, and what lets `destroy`
remove exactly what it made.

Consequences:

- State contains secrets (the kubeconfig is in there in plaintext). It is gitignored.
- Lose the state and Terraform forgets the resources exist. They keep running and billing. It
  will propose creating duplicates. Recovery is `terraform import`, one resource at a time.
- Two people applying against one state file corrupt it. Remote backends add locking. Ours is
  local because one person, one laptop. Moving it to an Azure Storage backend is the first
  thing to do if that stops being true.

### Reading a plan

```
+   create
-   destroy
~   update in place
-/+ destroy and recreate        ← the one to read twice
```

`-/+` happens when an argument cannot change on a live resource. We met it changing the
resource group's region: harmless on an empty group, data loss on one holding a database.
`lifecycle { prevent_destroy = true }` on anything stateful makes Terraform refuse such a plan.

### Two roots, not one

`infra/terraform/aks/` creates the cluster. `infra/terraform/platform/` installs things into it
(ingress-nginx, cert-manager, ArgoCD, the DNS zone) and reads the cluster through a data
source. They are separate because the Helm and Kubernetes providers need cluster credentials to
*configure themselves*, and a provider cannot be configured from a resource in the same root
that doesn't exist yet. It works exactly once on first apply and breaks on destroy or replace.

**Try it:**
```sh
cd infra/terraform/aks && terraform plan          # should say "No changes"
terraform state list                              # what Terraform believes it owns
terraform graph | grep -- '->'                    # dependency edges inferred from references
cd ../platform && terraform state list
```

### The region incident

First apply failed with `RequestDisallowedByAzure`. Not quota: an **Azure Policy** attached to
student subscriptions whitelisting five regions. Quota says how much; policy says whether at
all; they are checked in that order. The allowed list came from:

```sh
az policy assignment list --disable-scope-strict-match \
  --query "[].{name:displayName,params:parameters}" -o json
```

Three of the five were new datacentres without B-series VMs. Hyderabad had AKS, the `B2s` SKU
unrestricted, and quota. One variable changed and the plan showed `-/+` on the resource group.

---

## 4. The cluster

### What AKS gives you

A **control plane** (API server, scheduler, controllers, etcd) that Microsoft runs in its own
network, and a **node pool** of VMs you pay for. Free tier means no charge for the control plane
and no SLA on it. `konnectivity-agent` in `kube-system` is the tunnel the control plane uses to
reach your nodes, because it cannot see them directly.

### Requests, limits, and why 59% was already gone

```sh
kubectl describe node | grep -A6 "Allocated resources"
```

Before anything of ours was deployed, system pods had **requested** 1127m of ~1900m
allocatable CPU. A request is a reservation the scheduler honours when placing pods; it is not
usage. A limit is a ceiling the kernel enforces (CPU throttled, memory OOM-killed).

Rules that follow:

- Every container has a request, or the scheduler is guessing.
- Memory limits always; a leak without one takes the node down.
- CPU limits rarely; they throttle bursty code for no safety gain.
- A pod stuck in `Pending` with `Insufficient cpu` in `describe` means the node's requests are
  full, regardless of how idle it looks. The fix is lower requests or another node.

### The kubeconfig

`terraform output -raw kube_config > ~/.kube/optifuse` wrote a client certificate with
cluster-admin rights. Anyone holding that file is you. `chmod 600`, never in Git, never in a
screenshot.

**Try it:** `kubectl get pods -A` and read each `kube-system` pod's name against what it does:
`coredns` (cluster DNS), `azure-cns` (pod networking), `csi-azuredisk-node` (attaches disks),
`metrics-server` (feeds `kubectl top`), `azure-wi-webhook` (workload identity injector, section
8).

---

## 5. Networking

### Pods, Services, DNS

Every pod gets its own IP, routable from any other pod. Pods die and come back with new IPs,
so nothing addresses a pod directly. A **Service** is a stable name and virtual IP in front of
whichever pods currently match a label selector.

```sh
kubectl -n optifuse get svc
kubectl -n optifuse get endpointslice          # the actual pod IPs behind each Service
```

The gateway dials `parser:50051`. CoreDNS resolves `parser` to the Service IP because the pod's
`/etc/resolv.conf` has `optifuse.svc.cluster.local` on its search path. The full name is
`parser.optifuse.svc.cluster.local`. There is no proxy process: the virtual IP is iptables rules
on every node, written by `kube-proxy`.

The Postgres Service is **headless** (`clusterIP: None`): DNS returns the pod's own IP rather
than a virtual one. That is the convention for StatefulSets, where the identity of the specific
pod matters.

### How a load balancer appears

`ingress-nginx`'s Service is `type: LoadBalancer`. The Azure cloud controller watches for that
type, creates a Standard Load Balancer and a public IP, and writes the IP back into the
Service's status. That IP is `172.198.227.215`, and the DNS record in Terraform reads it from
the Service, so it is never typed by hand.

```sh
kubectl -n ingress-nginx get svc ingress-nginx-controller -w   # watch EXTERNAL-IP during a recreate
```

One annotation in `infra/terraform/platform/ingress.tf` matters more than it looks: Azure's
load balancer health-probes the node on `/` by default. nginx answers 404 there, the balancer
marks the backend unhealthy, and you have an IP that connects to nothing. Pointing the probe at
`/healthz` is the fix, and "AKS gave me an IP but nothing answers" is nearly always this.

### Ingress

The load balancer sends everything to nginx. nginx reads **Ingress** objects and routes by
`Host` header and path. One IP, any number of hostnames. `deploy/base/ingress.yaml` routes
`api.optifuse.vaivaswat.me` to the gateway Service. Note `proxy-read-timeout: "130"`: nginx
gives up on a backend after 60 s by default, and a live simulation is budgeted 120 s in
`simulate.go`. Without the annotation, slow runs 504 at the edge while the gateway completes
them.

### Reading nginx's status codes

- **404** from nginx: request reached nginx, no Ingress rule matched. What you see before any
  app is deployed. Healthy.
- **503** from nginx: rule matched, but the backend Service has **no ready endpoints**. The pods
  are failing readiness, or don't exist.
- **502**: a pod accepted the connection and then broke it.
- **504**: backend took longer than `proxy-read-timeout`.

**Try it:** scale the gateway to zero and curl the API: `kubectl -n optifuse scale deploy/gateway --replicas=0`, `curl -i https://api.optifuse.vaivaswat.me/healthz`. Expect 503. Then scale back to 1 (or wait for ArgoCD to do it; section 12).

---

## 6. DNS and TLS

### Delegation

`vaivaswat.me` stays at Namecheap because the portfolio (Vercel) and email forwarding live
there, and Namecheap's forwarding only works while Namecheap's nameservers serve the zone. So
one subdomain is **delegated**: four `NS` records at Namecheap say "for `optifuse`, ask Azure".
Azure DNS then owns `*.optifuse.vaivaswat.me` and nothing else. `.me` delegates `vaivaswat` to
Namecheap the same way; it is delegation all the way down.

```sh
dig +short NS optifuse.vaivaswat.me                          # the four Azure servers
dig +short @ns1-02.azure-dns.com api.optifuse.vaivaswat.me   # ask Azure directly, bypassing caches
dig +trace api.optifuse.vaivaswat.me | tail -12              # watch the whole delegation chain
```

### The apex rule

`optifuse.vaivaswat.me` itself is the apex of the delegated zone, and the frontend lives there.
Vercel asked for a CNAME. An apex **cannot be a CNAME**: it already carries SOA and NS records,
and a CNAME means "nothing else exists at this name". So it is an A record to Vercel's anycast
IP, the same thing Vercel does for every root domain.

### Certificates

cert-manager watches Ingress objects annotated `cert-manager.io/cluster-issuer`. For each, it
creates a Certificate, asks Let's Encrypt for one, and proves control of the name with the
**HTTP-01 challenge**: Let's Encrypt fetches
`http://<host>/.well-known/acme-challenge/<token>`, cert-manager serves the token through a
temporary pod routed via the same ingress. The signed cert lands in a Secret that nginx serves.
Renewal is automatic 30 days before expiry.

Two `ClusterIssuer`s exist (`deploy/cluster/letsencrypt.yaml`). Production rate-limits failed
validations to 5 per hostname per hour. When a certificate will not issue, switch the Ingress
annotation to `letsencrypt-staging`, which is unlimited but signed by an untrusted root, debug,
then switch back.

```sh
kubectl -n optifuse get certificate,certificaterequest,order,challenge   # the ACME state machine
kubectl -n optifuse describe certificate api-optifuse-tls                 # events tell you where it is stuck
curl -v https://api.optifuse.vaivaswat.me/healthz 2>&1 | grep -E "issuer|expire"
```

---

## 7. Workloads

### Deployment vs StatefulSet

A **Deployment** manages interchangeable pods: any replica is as good as any other, a new one
gets a random name, rolling updates replace them gradually. The four services are Deployments.

A **StatefulSet** manages pods with identity: stable names (`postgres-0`), stable storage (a
PersistentVolumeClaim per pod that survives rescheduling), ordered startup. Postgres is one.

### Probes: two different questions

```yaml
livenessProbe:  httpGet /healthz    # is the process wedged? fail → restart the pod
readinessProbe: httpGet /readyz     # can it serve right now? fail → remove from Service endpoints
```

`/healthz` returns 200 unconditionally. `/readyz` pings the database. **Liveness must never
check a dependency**: if it checked the database, one database blip would restart every gateway
at once, turning a partial outage into a total one. Readiness checking the database is exactly
right: a gateway that lost its database stops receiving traffic without being killed.

The gRPC services use Kubernetes' native `grpc:` probe, which calls the standard health service
that `shared/grpcserver` registers. On SIGTERM the server flips to `NOT_SERVING`, readiness
fails, the pod leaves the endpoints, and in-flight requests finish during the 2 s drain. That is
the zero-dropped-requests rollout.

`terminationGracePeriodSeconds: 30` matches the code's 2 s drain + 25 s graceful stop.
Kubernetes sends SIGKILL when it runs out.

**Try it:** `kubectl -n optifuse rollout restart deploy/gateway` while running
`while true; do curl -s -o /dev/null -w "%{http_code}\n" https://api.optifuse.vaivaswat.me/healthz; sleep 0.2; done`
in another terminal. Count the non-200s. There should be none.

### Security context and Pod Security Admission

The namespace carries `pod-security.kubernetes.io/enforce: restricted`. The API server rejects
any pod in it that runs as root, keeps Linux capabilities, allows privilege escalation, or lacks
a seccomp profile. Every manifest in `deploy/base` is written to pass:

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 10001                  # the user the Dockerfile created
  seccompProfile: { type: RuntimeDefault }
containers[].securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities: { drop: ["ALL"] }
```

`readOnlyRootFilesystem` has a consequence: anything that writes needs a volume. The optimizer
writes LP files to `/tmp` for `glpsol`, so `/tmp` is an `emptyDir`. Postgres needs
`/var/run/postgresql` for its socket. Try removing one of those mounts and the pod will crash
with a permission error, which is the control working.

**Try it:** `kubectl -n optifuse run test --image=nginx --restart=Never`. It is refused, with a
message listing every violated rule. That is PSA.

### Storage

The StatefulSet's `volumeClaimTemplates` creates a PersistentVolumeClaim; the Azure Disk CSI
driver provisions a managed disk and attaches it to the node. Two details that cost people
hours:

- `fsGroup: 70`: the disk mounts owned by root; `fsGroup` makes the kubelet chown it to the
  Postgres group on attach.
- `PGDATA=/var/lib/postgresql/data/pgdata`: a fresh disk has `lost+found` at its root, and
  `initdb` refuses a non-empty directory. A subdirectory sidesteps it.

```sh
kubectl -n optifuse get pvc,pv
az disk list -g MC_rg-optifuse-dev_aks-optifuse-dev_indiasouthcentral -o table   # the real disk
```

That `MC_…` resource group is created by AKS for node-level resources (VM scale set, load
balancer, disks, public IPs). You do not manage it; the cluster does.

---

## 8. Configuration and secrets

**ConfigMap** for anything that can be read by anyone with namespace access: log level,
service addresses, AWS region. Plain text in Git. **Secret** for the rest. A Secret is base64 in
etcd (not encryption; AKS encrypts etcd at rest on its side), readable by anyone with
`get secrets` RBAC in the namespace. That is the honest level of protection.

Both are injected with `envFrom`, so the Go code reads `os.Getenv` exactly as under Compose.

The three Secrets were created with `kubectl create secret generic … --from-env-file`, reading
local files, so no value passed through a shell history or a chat. They are not in Git. To
recreate them on a fresh cluster, the commands are in section 14.

The enricher's AWS keys are the one Secret that should not exist. AKS publishes an **OIDC
issuer** (`terraform output oidc_issuer_url` in `aks/`). AWS IAM can trust any OIDC issuer, so a
pod's projected service-account token can be exchanged for AWS credentials with
`AssumeRoleWithWebIdentity`, no key stored anywhere. The AWS SDK's default credential chain
picks it up with no code change. `azure-wi-webhook` in `kube-system` is the component that
injects the token. This is Milestone 4.

**Try it:** `kubectl -n optifuse get secret gateway -o jsonpath='{.data}' | jq 'keys'` shows
the keys. Decode one and you will see why RBAC on secrets matters.

---

## 9. Kustomize

`deploy/base/` is the application as it should exist everywhere. `deploy/overlays/dev/` is
what differs here: the image tags. A `prod` overlay would add replicas, resources, a different
hostname, and share the same base. `kubectl kustomize deploy/overlays/dev` renders the result;
`kubectl apply -k` applies it.

Two behaviours that shaped the layout:

- The `namespace:` transformer stamps the namespace onto **every** kind, including CRDs it does
  not recognise. A cluster-scoped `ClusterIssuer` in the base would get a namespace it cannot
  have. Hence `deploy/cluster/`, applied separately.
- Kustomize refuses to read files outside the kustomization's directory (the "load
  restrictor"). That is why `schema.sql` could not be mounted from `services/gateway/…` and
  moved into the binary instead (section 10).

**Try it:** `kubectl kustomize deploy/overlays/dev | grep image:` to see the pinned tags.
Change a tag in the overlay, run `kubectl diff -k deploy/overlays/dev`, and read what would
change. Then `git checkout` the file.

---

## 10. Schema on startup

Locally, Postgres got its tables from `schema.sql` mounted into the image's init directory,
which runs once on an empty data directory. Kubernetes could not mount the file (section 9), and
copying it would mean two schemas drifting. Since every statement was already `IF NOT EXISTS`,
the gateway now embeds it (`//go:embed schema.sql` in `services/gateway/internal/db/queries.go`)
and applies it on every connect, in one transaction, holding `pg_advisory_xact_lock`.

The lock exists because two gateways starting simultaneously against an empty database would
race on `CREATE EXTENSION`, which is not safe to run concurrently even with `IF NOT EXISTS`.
`schema_test.go` starts four at once to prove it. Run it against a throwaway Postgres:

```sh
docker run --rm -d --name pgtest -p 55432:5432 -e POSTGRES_PASSWORD=x postgres:15-alpine
OPTIFUSE_TEST_DATABASE_URL=postgres://postgres:x@localhost:55432/postgres go test -v ./services/gateway/internal/db/
docker rm -f pgtest
```

---

## 11. Auth across two domains

Three things had to agree, and two of them were wrong on the first try:

1. **CORS.** The gateway allows one origin, `CLIENT_ORIGIN_URL`. A browser sends a preflight
   `OPTIONS` before any cross-origin `POST` with an `Authorization` header; if the response's
   `Access-Control-Allow-Origin` does not match the page's origin exactly, the browser refuses
   to send the real request. Test it as the browser would:
   ```sh
   curl -si -X OPTIONS -H "Origin: https://optifuse.vaivaswat.me" \
     -H "Access-Control-Request-Method: POST" \
     https://api.optifuse.vaivaswat.me/api/auth/github/ | grep -i access-control
   ```
2. **GitHub OAuth `redirect_uri`.** An OAuth app may register several callback URLs. If the
   authorize request omits `redirect_uri`, GitHub uses the **first** registered one. The login
   page used to omit it, so a login started on the new domain came back on the Vercel domain,
   whose origin CORS then refused. The browser console said exactly this. The fix
   (`src/app/login/github-login-button.tsx`) sends `redirect_uri` built from
   `window.location.origin`.
3. **The external ID.** Every fresh database generates a new `aws_external_id` per profile;
   the customer's CloudFormation role trusts the old one. Symptom: enrichment silently skipped
   (no ARN) or AssumeRole 403 (wrong ID). The gateway now reports `telemetry: estimates` and a
   warning when this happens, and the UI shows it, so a guess is never labelled a measurement.

---

## 12. GitOps with ArgoCD

### The idea

Until now, `kubectl apply` from a laptop was how the cluster learned what to run. That means
the laptop holds cluster-admin, the cluster's state is whatever was last applied from wherever,
and nothing notices if someone edits a Deployment by hand.

ArgoCD inverts it. An **Application** object says "this Git path, this cluster, this
namespace". ArgoCD renders the path (it understands Kustomize), diffs it against the live
objects, and applies the difference. Continuously. `prune: true` deletes what Git no longer
mentions; `selfHeal: true` reverts what someone changed by hand. Git is the source of truth,
`git log` is the audit trail, `git revert` is the rollback.

It lives in the cluster, so **nothing outside the cluster needs a kubeconfig**. CI (section 13)
only writes to Git.

### Bootstrapping it (you do this)

ArgoCD is installed (`infra/terraform/platform/argocd.tf`). The Application is written but not
applied. Apply it once; it is the last `kubectl apply` of application manifests you should ever
need:

```sh
kubectl apply -f deploy/argocd/optifuse.yaml
kubectl -n argocd get application optifuse -w
```

Expect `SYNC STATUS` to go `OutOfSync → Synced` and `HEALTH` to `Healthy` within a minute.
ArgoCD adopts the running objects; nothing restarts, because the rendered manifests match what
`kubectl apply -k` created.

Then get into the UI. The admin password is generated at install:

```sh
kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d; echo
```

Open `https://argocd.optifuse.vaivaswat.me`, user `admin`. The certificate came from the same
issuer as the API's. Click into the `optifuse` application and you will see the resource tree:
Namespace, ConfigMap, Services, Deployments, ReplicaSets, Pods, the Ingress, the Certificate.

### The experiment that explains GitOps better than any paragraph

Predict first, then run:

```sh
kubectl -n optifuse delete deployment gateway
kubectl -n optifuse get deploy -w
```

What do you think happens, and how long does it take? Then do it. Then try the subtler one:

```sh
kubectl -n optifuse scale deploy/parser --replicas=3
kubectl -n optifuse get deploy parser -w
```

Git says `replicas: 1`. Watch how long your edit survives. That is `selfHeal`. It is also why,
in a GitOps cluster, `kubectl edit` is a diagnostic tool and never a fix.

### Things to notice in the UI

- The **diff** view on any resource shows live vs desired. Secrets appear as "not managed"
  because they are not in Git.
- **History and rollback** lists every synced commit. Rollback there is a UI convenience; the
  real rollback is `git revert` followed by a sync.
- `applicationset-controller` is running even though the chart values tried to disable it.
  Find the right key: `helm show values argo/argo-cd | grep -n -i -B2 -A2 applicationset`.
  Fix it in `argocd.tf`, plan, apply. Your first Terraform change on your own.

---

## 13. CI

`.github/workflows/ci.yml`, three jobs:

1. **test**: `gofmt`, `go vet`, `go test`. Installs `glpk-utils` so the MtxILP round-trip tests
   run instead of skipping. Runs on pull requests too.
2. **build**: one matrix leg per service. Logs into GHCR with the workflow's own
   `GITHUB_TOKEN` (scoped to `packages: write` for the run; nothing to rotate), builds with
   `docker/build-push-action`, tags with the short SHA, and caches layers in the registry so a
   one-service change rebuilds only that service.
3. **bump**: `kustomize edit set image` in `deploy/overlays/dev`, commit, push to `main`.

Three design points worth being able to defend:

- **Triggers on code paths only.** The bump commit touches `deploy/overlays/` alone, so it
  cannot trigger another build. Without the path filter: infinite loop.
- **`concurrency: bump-dev-overlay`** so two quick pushes don't race each other committing to
  `main`.
- **CI never contacts the cluster.** No kubeconfig in GitHub, no `kubectl` in the workflow.
  Compromise CI and the attacker can push an image tag; they cannot run anything. The cluster
  pulls what Git says, via ArgoCD.

### Watching one commit go all the way (you do this)

The workflow is written but not pushed. Commit and push it yourself:

```sh
git add .github/ deploy/argocd/ infra/terraform/platform/argocd.tf docs/ README.md
git commit -m "ci, argocd, deployment guide"
git push origin main
```

That push changes `.github/workflows/ci.yml`, which is in the trigger paths, so it runs. Watch:

```sh
gh run watch                                    # or the Actions tab on GitHub
git pull                                        # after ~5 min: the bot's "deploy: pin images to …" commit
kubectl -n argocd get application optifuse -w   # OutOfSync → Synced
kubectl -n optifuse get pods -w                 # new ReplicaSets rolling in
kubectl -n optifuse logs deploy/gateway | head -1   # "version": the new SHA
```

Then prove it with a change you can see: edit a log message in `services/gateway/cmd/main.go`,
commit, push, and do nothing else. Five minutes later, read the new message in the pod's log.
Nobody ran `kubectl`.

---

## 14. Day-2: the commands you will actually use

```sh
# What is running, and is it healthy
kubectl -n optifuse get pods -o wide
kubectl -n optifuse describe pod <name>              # events at the bottom: pulls, probes, OOM, scheduling
kubectl -n optifuse get events --sort-by=.lastTimestamp

# Logs (JSON; jq is your friend)
kubectl -n optifuse logs deploy/gateway --since=10m -f
kubectl -n optifuse logs deploy/gateway --previous    # the container before the last restart
kubectl -n optifuse logs -l app.kubernetes.io/part-of=optifuse --prefix --since=5m
kubectl -n optifuse logs deploy/gateway | jq -r 'select(.msg=="request") | "\(.status) \(.method) \(.path)"'

# Inside a container
kubectl -n optifuse exec -it deploy/gateway -- sh
kubectl -n optifuse exec postgres-0 -- psql -U optifuse -d optifuse_db -c '\dt'

# Reach an internal port from your laptop
kubectl -n optifuse port-forward svc/gateway 9090:9090   # then curl localhost:9090/metrics

# Resource usage (actual, not requests)
kubectl top nodes
kubectl top pods -n optifuse

# Rollouts
kubectl -n optifuse rollout status deploy/gateway
kubectl -n optifuse rollout history deploy/gateway
```

### Recreating the secrets on a fresh cluster

```sh
kubectl apply -f deploy/base/namespace.yaml
PGPW=$(openssl rand -hex 24)
kubectl -n optifuse create secret generic postgres --from-literal=password="$PGPW"
{ echo "DATABASE_URL=postgres://optifuse:${PGPW}@postgres:5432/optifuse_db?sslmode=disable";
  grep -E '^(GITHUB_CLIENT_ID|GITHUB_CLIENT_SECRET|CLIENT_ORIGIN_URL)=' ~/optifuse-prod.env; } > /tmp/gw.env
kubectl -n optifuse create secret generic gateway --from-env-file=/tmp/gw.env && shred -u /tmp/gw.env
kubectl -n optifuse create secret generic enricher-aws \
  --from-env-file=<(grep -E '^(AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY)=' .env)
unset PGPW
```

After the first login, fix the profile row's external ID to the one the CloudFormation role
trusts (section 11, item 3).

### Stopping the bill

```sh
cd infra/terraform/platform && terraform destroy    # LB, public IP, ArgoCD, zone (NS records at Namecheap go stale, harmless)
cd ../aks && terraform destroy                       # cluster, node, disks
```

Bringing it back: `apply` both, recreate secrets, `kubectl apply -f deploy/argocd/optifuse.yaml`.
Fifteen minutes. The database is lost; it is one login and one row update to recreate.

Current run rate: about $1.80/day (node ≈ $1, load balancer + IP ≈ $0.70, disk ≈ $0.10).

---

## 15. The incident log

Every one of these was diagnosed from a symptom. The method transfers; the fixes don't.

| Symptom | What it ruled in/out | Root cause |
|---|---|---|
| Simulation returned in 350 ms | Logs Insights takes seconds; enricher logged nothing | Empty role ARN → enrichment skipped silently |
| `RequestDisallowedByAzure` on apply | Not a quota message; quota had been checked | Azure Policy region whitelist on student subs |
| 404 from the load balancer IP | Traffic reached nginx (a timeout would mean the LB probe failed) | Expected: no Ingress rules yet |
| 4 pods `ErrImagePull` | `describe pod` showed 401 from GHCR | New packages default to private |
| Login did nothing; gateway saw no POST | Failure was before the gateway | CORS error in console: wrong origin, because GitHub used the first registered callback |
| "redirect_uri is not associated" | Request was right (read from the bundle) | URI not saved on the OAuth app |
| Deployed results used YAML numbers under a "live" heading | Enricher log empty for the request | Fresh DB, new profile row, no ARN. Fixed the row; fixed the UI to say which source it used |
| Plan showed `-/+` on the resource group | `location` forces replacement | Expected, and worth reading twice on anything stateful |

---

## 16. Next: observability, built by you

The services already expose Prometheus metrics on `:9090/metrics` (RED metrics per route and
gRPC method, algorithm durations and failures, which algorithm wins, enrichment coverage,
GitHub API latency). Nothing scrapes them yet. The milestone is: Prometheus scraping, Grafana
showing, and a dashboard you can point at.

### Concepts you will meet, at the point of use

- **Pull-based scraping.** Prometheus fetches `/metrics` on a schedule; services push nothing.
  So the question is always "how does Prometheus find the targets", and the answer in
  Kubernetes is service discovery via labels.
- **ServiceMonitor.** A CRD from the Prometheus Operator: "scrape the Services matching these
  labels, on this port name, this often". One per service, or one for all four since they share
  a `part-of` label and a `metrics` port name.
- **Cardinality.** Every distinct label combination is a time series in memory. A label with
  unbounded values (user ID, request ID, raw error string) will eventually take Prometheus
  down. The services already classify error strings and slug algorithm names for this reason.
- **PromQL.** `rate(counter[5m])` turns a counter into a per-second rate.
  `histogram_quantile(0.99, sum by (le) (rate(bucket[5m])))` turns a histogram into a p99.
  Those two cover most dashboards.

### Steps

**1. Look at what you are about to scrape.**
```sh
kubectl -n optifuse port-forward svc/gateway 9090:9090 &
curl -s localhost:9090/metrics | grep -v '^#' | cut -d'{' -f1 | sort -u
kill %1
```
Read the metric names. For each, decide: counter, gauge, or histogram? What would you plot?

**2. Install kube-prometheus-stack**, trimmed for one node. Write
`infra/terraform/platform/monitoring.tf` yourself, modelled on `argocd.tf`:
chart `prometheus-community/kube-prometheus-stack`, namespace `monitoring`. Values to set and
why:
- `prometheus.prometheusSpec.retention: 6h` and `resources.requests.memory: 400Mi`. Prometheus
  memory scales with series × retention; a B2s cannot hold days.
- `alertmanager.enabled: false` for now.
- `grafana.ingress` on `grafana.optifuse.vaivaswat.me` with the same cert-manager annotation
  (the wildcard DNS record already resolves it). `grafana.adminPassword` from a variable marked
  `sensitive`.
- `prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues: false`, or Prometheus
  will only scrape ServiceMonitors carrying the chart's release label and ignore yours. This
  single setting is the most common "my ServiceMonitor does nothing" cause.

`terraform plan` and read it before `apply`. Expect the node to get tight: this is the most
likely point for a `Pending` pod with `Insufficient memory`, and the second `B2s` node is one
line in `aks/variables.tf` (`node_count`), one plan, one apply.

**3. Write the ServiceMonitor** in `deploy/base/servicemonitor.yaml`, add it to the
kustomization, commit, push, and let ArgoCD apply it. Then in Prometheus
(`kubectl -n monitoring port-forward svc/prometheus-operated 9091:9090`, open
`localhost:9091/targets`) find your four targets. If they are missing, the selector label or
the port name is wrong; `kubectl -n monitoring logs prometheus-… -c prometheus` says which.

**4. Generate traffic you can see**: run a few simulations from the UI, then in Prometheus
query `sum by (route, code) (rate(http_server_requests_total[5m]))` for the gateway, and
`histogram_quantile(0.99, sum by (le, route) (rate(http_server_request_duration_seconds_bucket[5m])))`
for its p99. The algorithm and enrichment metrics live on the optimizer and enricher
(`:9090` on each); port-forward to those to read their names.

**5. Build the dashboard** in Grafana by hand first, panel by panel:
- Request rate and error rate per route (RED: Rate, Errors)
- p50/p99 latency per route (Duration)
- Algorithm wall-clock per algorithm, and failures
- Which algorithm is cheapest, as a share over time
- Enrichment: fraction of functions with telemetry vs missing
- Node CPU and memory requests vs allocatable (the number you have been watching by hand)

Then export it as JSON into `deploy/base/grafana-dashboard.yaml` as a ConfigMap labelled
`grafana_dashboard: "1"`; the chart's sidecar picks it up, and the dashboard becomes code
that ArgoCD deploys like everything else.

**6. Make it public.** Grafana supports public dashboards (share → public). Link it from the
README. A dashboard someone can open is worth more than a paragraph saying one exists.

When you have a target showing in Prometheus, or a panel that does not show what you expected,
that is the moment to come back with the specific thing on screen.

---

## 17. Exercise checklist

- [ ] `terraform plan` in both roots shows no changes
- [ ] Explain, without looking, why liveness must not check the database
- [ ] Scale the gateway to 0, observe 503, watch ArgoCD restore it
- [ ] Delete the gateway Deployment and time its return
- [ ] Rolling restart with a curl loop running: zero non-200s
- [ ] Try to run a root container in `optifuse`; read the PSA refusal
- [ ] Switch the Ingress to `letsencrypt-staging`, watch the cert reissue, switch back
- [ ] Find and fix the `applicationSet` chart key in `argocd.tf`
- [ ] Push the workflow and watch a commit become a running pod with no `kubectl`
- [ ] Change a log line, push, read it in the pod five minutes later
- [ ] `dig +trace` the API hostname and name every server in the chain
- [ ] Decode a Secret and explain to yourself why that was too easy
- [ ] Section 16, start to finish
