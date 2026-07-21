# Write-up — Run a Service the QOVES Way

The reasoning behind the build. Operational steps are in [README.md](README.md).
The five required sections are below, followed by the storage (F) and scaling (G)
analysis and a note on the supply-chain stretch.

---

## 1. Run it

**From scratch (summary; full commands in the README).** Start a 2-node minikube
cluster on **Calico**; enable the `ingress` and `metrics-server` addons; build,
push and **Cosign-sign** the API image to GHCR; install Argo CD by hand; then
`kubectl apply` the **one** root Application. Everything after that is GitOps.
Provide the DB credential by restoring the Sealed Secrets controller key (or
re-sealing). On the Docker driver, reach the ingress via `minikube tunnel`.

**Repo layout.**

```text
app/                # the API (Go) + Dockerfile — the only thing we run that we wrote
charts/api/         # Helm chart: Deployment, Service, Ingress, HPA, ServiceMonitor, PrometheusRule
gitops/bootstrap/   # root Application (app-of-apps)
gitops/apps/        # one child Application per component, ordered by sync-wave
network-policies/   # default-deny + explicit allows
secrets/            # SealedSecret (ciphertext only)
values/             # values for upstream charts (postgres, monitoring, kyverno)
supply-chain/       # Kyverno policy + Cosign public key
docs/               # this write-up + README
```

The layout is deliberately 1:1 with the Argo CD app tree: one directory (or one
upstream chart reference) per child Application.

**How to make a change (the GitOps flow).** Change a value in git → commit → push.
Argo CD reconciles the cluster to match: `selfHeal` corrects manual drift, `prune`
deletes what you removed. There is no `kubectl apply`/`edit` on workloads — the
cluster is a projection of the repo. The one imperative bootstrap (Argo CD itself,
the minikube addons) is allowed because a controller cannot install itself.

---

## 2. Decisions (ADRs)

### CNI — Calico
**Decision.** Calico (`minikube start --cni=calico`).
**Alternatives.** Cilium; Flannel; minikube's default (kindnet/bridge).
**Why.** The default CNI **does not enforce NetworkPolicy** — policies apply and
silently do nothing, the trap the brief calls out. Calico enforces policy, is
mature, and is light enough for minikube. Cilium is an equally valid, richer choice
(eBPF, L7 policy, Hubble) but heavier than this needs. Flannel is disqualified — no
enforcement. The transferable point: *pick a CNI that actually enforces the
security boundary you rely on.*

### Secrets — Sealed Secrets
**Decision.** Sealed Secrets: encrypt the credential into git; the in-cluster
controller decrypts it into a normal `Secret`.
**Alternatives.** SOPS (needs an Argo plugin); External Secrets Operator + Vault;
plain base64 (fails the brief).
**Why.** Simplest fully-local option that keeps plaintext out of a **public** repo
while staying GitOps-native. The trade-off is **key management**: the controller's
private key *is* the recoverable asset — lose it and every `SealedSecret` becomes
undecryptable. We hit exactly this rebuilding the cluster and recovered by
**restoring the controller key from the old cluster** rather than re-sealing (see
the runbook). Maps cleanly onto **ESO + a managed store** (Vault/cloud) in
production.
**Delivering `DATABASE_URL`.** Rather than sealing a second copy of the password,
the Deployment pulls only the password from the existing SealedSecret and assembles
the URL at runtime via env interpolation
(`postgresql://qvs:$(DB_PASSWORD)@…/qvs`). Non-secret parts are plain values; the
secret is referenced once. Alternative: a dedicated `DATABASE_URL` SealedSecret —
cleaner conceptually but duplicates the credential and forces a re-seal on every
password change.

### PostgreSQL — Bitnami chart (standalone), not an operator
**Decision.** Bitnami Helm chart, `architecture: standalone`, with a PVC.
**Alternatives.** CloudNativePG / Crunchy operator; raw StatefulSet.
**Why.** The brief needs one durable instance; the chart gives a
production-shaped StatefulSet (persistence, metrics exporter, hardened
securityContext) without operator complexity. An operator (CloudNativePG) is the
right call the moment you want **replication, failover, and backup/PITR** — the
documented production path and a stretch goal, not day-one complexity.

### Scaling signal — CPU HPA (with a caveat)
**Decision.** HPA on CPU utilisation (2–5 replicas, target 70%).
**Alternatives.** Memory; requests-per-second / concurrency (KEDA or
prometheus-adapter); latency SLO.
**Why / honest caveat.** CPU is the native, safe default and satisfies the
requirement, but it is **not the ideal signal for this API**: its work is dominated
by a DB round-trip, so it is I/O-bound — it spends most of its time *waiting*, not
burning CPU. Under load, concurrency and latency rise before CPU does, so a CPU HPA
scales late. The correct production signal is **requests-per-second or in-flight
concurrency** (KEDA / prometheus-adapter over the `/metrics` we expose), or a
**p95-latency SLO** — bounded by the fact that scaling the API is pointless past
the **single Postgres instance's** connection/throughput limit, the real
bottleneck.

### Supply chain — Cosign + Kyverno
**Decision.** Publish the image to GHCR pinned **by digest**, sign it with Cosign,
and enforce signature verification at admission with a Kyverno `ClusterPolicy`
(`Enforce`).
**Alternatives.** Notary/Notation; Connaisseur; policy-only (no signing).
**Why.** Signing + admission verification closes the "only trusted images run" gap:
a tampered or unsigned image with the same tag is rejected before it schedules
(demonstrated by pushing an unsigned tag and watching admission deny it). The
public key lives in git; the private key never does. In production the signing step
moves into CI and the policy widens to cover all namespaces.

---

## 3. What minikube did for me

On real bare metal I would build and operate each of these; minikube handed them
over for free:

| minikube gave me | What I'd build/run on bare metal |
|------------------|----------------------------------|
| **Control-plane bootstrap** (`minikube start`) | `kubeadm init` / Cluster API: PKI + certs, control-plane static pods, kubeconfigs, join tokens |
| **CNI install** (`--cni=calico`) | Install/operate Calico myself: operator/daemonset, IPAM, pod-CIDR routing, BGP or overlay |
| **Ingress "load balancing"** (`ingress` addon + `minikube tunnel` on 127.0.0.1) | A real ingress controller behind **MetalLB** (L2/BGP) or a cloud LB, plus TLS + DNS |
| **Storage provisioner** (`standard` StorageClass, dynamic PVCs) | A CSI driver (Ceph/Rook, Longhorn, cloud EBS/PD): provisioning, attach/detach, resize, snapshots |
| **etcd + its lifecycle** | Run etcd as an HA quorum (3/5) with TLS, defrag/compaction, and **regular `etcdctl snapshot save` backups** + tested restore |

The theme: minikube collapses the entire "who runs the platform" layer into one
binary. On bare metal that layer is the job.

---

## 4. Production gaps

- **HA control plane.** Single control plane + single etcd. Need a stacked/external
  etcd quorum and multiple API servers behind a LB.
- **Database HA + backups.** One standalone Postgres on a node-local volume = a SPOF
  with no recovery. Move to **CloudNativePG**: replicas, automated failover, and
  continuous WAL archiving + base backups to object storage (MinIO/S3) with PITR.
- **A real secret backend.** Sealed Secrets is fine locally but its controller key
  is a single recoverable secret. Production: **ESO + Vault / cloud secret manager**
  with rotation and audit — and back up the Sealed Secrets key regardless.
- **Ingress + TLS + DNS.** Real LB (MetalLB/cloud), cert-manager for TLS, and DNS
  instead of `/etc/hosts` + `minikube tunnel`.
- **Upgrades.** A tested Kubernetes and app upgrade strategy (surge/drain, PDBs,
  canary/blue-green) — none exists yet.
- **Storage durability.** `standard` is node-local hostpath; a lost node loses the
  data. Need a networked CSI backend with snapshots.
- **Observability depth.** API + Postgres are scraped and one alert exists; still
  missing: dashboards, more SLO alerts, Alertmanager routing, and **log
  aggregation** (Loki/ELK).
- **Supply chain hardening.** Signing is manual today; move it into CI, widen the
  policy cluster-wide, and add SBOM + vulnerability scanning gates.
- **Multi-environment.** One cluster/namespace. Need dev/staging/prod separation
  (Argo projects, per-env overlays, a promotion flow).

---

## 5. Runbook — recover from a lost/rebuilt cluster (Sealed Secrets key)

**Failure mode.** The cluster was rebuilt (or the controller reinstalled). The
committed `SealedSecret` no longer decrypts because the new controller generated a
**new** key, so `qvs-postgresql-credentials` is never created — Postgres stays in
`Init` and the API reports `CreateContainerConfigError: secret not found`. This is a
real incident we hit.

**Why.** Sealed Secrets encrypts with a controller **public** key; only the matching
**private** key (cluster-only) decrypts. New controller = new key pair = old
ciphertext is unrecoverable by it.

**Recovery (declarative / through git where possible):**

1. **Confirm.**
   ```bash
   kubectl get secret qvs-postgresql-credentials -n qvs-app     # NotFound
   kubectl get crd sealedsecrets.bitnami.com                    # CRD present?
   kubectl get pods -n kube-system | grep sealed-secrets        # controller present?
   ```
2. **If the old cluster still exists — restore its key (no re-seal, no git change):**
   ```bash
   # old cluster
   kubectl get secret -n kube-system -l sealedsecrets.bitnami.com/sealed-secrets-key -o yaml > key.yaml
   # move key.yaml securely, then on the new cluster:
   kubectl apply -f key.yaml
   kubectl delete pod -n kube-system -l app.kubernetes.io/name=sealed-secrets   # reload keys
   ```
   The controller holds multiple keys and decrypts with whichever matches, so the
   committed ciphertext now unseals.
3. **If the key is gone — re-seal** fresh credentials against the new controller and
   push (README §Provide the database credential). The DB volume is uninitialised on
   a fresh cluster, so choose new passwords freely.
4. **Verify — Argo `selfHeal` needs nothing further:**
   ```bash
   kubectl get secret qvs-postgresql-credentials -n qvs-app     # now exists
   kubectl rollout status deploy/qvs-api -n qvs-app
   curl http://api.qvs.local/healthz                            # 200
   ```
5. **Prevent recurrence.** Back up the controller key to a secure store as DR; in
   production replace Sealed Secrets with ESO + a managed store.

> The `key.yaml` export is the **master decryption key** — never commit it, delete
> after import. It is `.gitignore`d as a safeguard.

*(Alternative runbook — "a bad deploy": `git revert <sha> && git push`, and Argo
rolls the cluster back to the previous desired state.)*

---

## F. Storage & data (reasoning)

- **Access mode & scheduling constraint.** The PVC (`data-qoves-postgresql-0`) is
  **ReadWriteOnce (RWO)** — mountable read-write by **one node at a time**. This
  pins the Postgres pod to the node holding the volume; you cannot spread
  read-write replicas of this StatefulSet across nodes sharing this volume. Fine for
  a single instance, but it is a hard blocker to HA and is why real HA Postgres uses
  replication (each replica its own volume), not a shared RWO volume.
- **If the pod dies vs. the node dies.** *Pod dies:* the StatefulSet recreates it,
  the same PVC re-attaches, data persists (verified: created a table, deleted the
  pod, the row survived). *Node dies:* with minikube's `standard` provisioner the
  volume is **local to that node's filesystem** — if the node is gone the data is
  unavailable/lost. A networked CSI backend (EBS, Ceph, Longhorn) can re-attach the
  volume on a surviving node, so node loss is survivable. This is the biggest
  storage risk here.
- **Backup & restore.** Pragmatic: a scheduled `pg_dump`/`pg_dumpall` CronJob to
  object storage (MinIO/S3), restored with `psql`/`pg_restore` — volume-independent.
  Infrastructure: **CSI VolumeSnapshots** for crash-consistent point-in-time copies.
  Production: **CloudNativePG** with continuous WAL archiving + base backups and PITR.

---

## G. Resources & scaling (reasoning)

- **Requests/limits.** API: `requests 100m/128Mi`, `limits 500m/256Mi` — small,
  matching a thin I/O-bound service, with burst headroom. Postgres: `requests
  100m/256Mi`, `limits 500m/512Mi`. Requests give the scheduler and HPA an honest
  baseline; limits cap blast radius. Right-sizing is the top cause of production
  instability, so these are explicit and justified rather than omitted or copied.
- **Is CPU the right HPA signal?** *No, not ideally* — see the ADR. The API is gated
  on a Postgres round-trip, so it is I/O-bound: concurrency and latency climb before
  CPU does, and a CPU HPA scales late. The correct signal is **requests-per-second
  or in-flight concurrency** (KEDA / prometheus-adapter over `/metrics`), or a
  **p95-latency SLO**. CPU is used because it is native, safe, and satisfies the
  requirement — and crucially, scaling the API only helps until the **single
  Postgres instance** becomes the bottleneck, so real scaling work must include the
  DB tier (PgBouncer pooling, read replicas), not just the API.

---

## H. Observability (implemented)

`kube-prometheus-stack` runs in `monitoring`. Both the **API** (`/metrics` on
`:8080`, via a `ServiceMonitor`) and **Postgres** (exporter on `:9187`) are scraped;
NetworkPolicies allow `monitoring → :8080/:9187` under default-deny. Grafana and
Alertmanager are disabled to keep the footprint minimal, per the brief — metrics are
inspected via the Prometheus expression browser.

**The one alert — `APIHealthzErrorRate`:**
```promql
sum(rate(http_requests_total{namespace="qvs-app",status=~"5.."}[5m]))
  / sum(rate(http_requests_total{namespace="qvs-app"}[5m])) > 0.1
```
*Rationale:* `/healthz` returns 503 when `SELECT 1` against Postgres fails, and the
readiness/liveness probes hit it continuously — so the 5xx ratio tracks DB health
even with no user traffic. A sustained ratio is the "service is down for users"
condition and is actionable (check DB pod, PVC, NetworkPolicies). When idle the
ratio is `NaN`, so it does not false-fire.
