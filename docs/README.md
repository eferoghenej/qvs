# QVS — Run a Service the QOVES Way (minikube)

A small HTTP API and its PostgreSQL database, run on a self-managed Kubernetes
cluster the way a platform team would: **GitOps delivery** (Argo CD),
**default-deny networking** (Calico), **secrets that never touch git** (Sealed
Secrets), pinned + **signature-verified** images (Cosign + Kyverno), resource
limits with autoscaling, and Prometheus monitoring with an alert.

> The reasoning — architecture decisions, storage/scaling analysis, "what
> minikube did for me", production gaps, and a runbook — is in
> [WRITEUP.md](WRITEUP.md). This file is the operational how-to.

---

## Architecture

```
  git push ──▶ GitHub (source of truth) ──▶ Argo CD (app-of-apps) ──▶ reconciles cluster
                                                                            │
   ┌────────────────────────── minikube (Calico CNI, 2 nodes) ─────────────┼───────────┐
   │                                                                        ▼           │
   │  ingress-nginx ─(:8080)─▶ ┌──────────────── qvs-app (default-deny) ───────────┐    │
   │      addon                │  API (Deployment, HPA 2–5)                        │    │
   │                           │    │  DATABASE_URL (password from Sealed Secret)  │    │
   │                           │    ▼ :5432                                        │    │
   │                           │  PostgreSQL (StatefulSet + PVC, RWO)              │    │
   │                           └──────────────────────────────────────────────────┘    │
   │  monitoring: kube-prometheus-stack ─(scrape API :8080 / pg :9187)                   │
   │  kube-system: sealed-secrets-controller     kyverno: Cosign image-verify (Enforce)  │
   └─────────────────────────────────────────────────────────────────────────────────┘
```

| Component | Choice | Role |
|-----------|--------|------|
| CNI | **Calico** | Enforces NetworkPolicy (minikube's default does not) |
| GitOps | **Argo CD** (app-of-apps) | Reconciles the whole stack from git |
| Database | **Bitnami PostgreSQL** (standalone + PVC) | Stateful backend |
| Secrets | **Sealed Secrets** | Encrypted credentials committed to git |
| Ingress | **minikube ingress addon** (ingress-nginx) | In-cluster HTTP entry point |
| Scaling | **HPA** (CPU) + metrics-server | Horizontal autoscaling of the API |
| Monitoring | **kube-prometheus-stack** | Scrapes `/metrics`; one alert rule |
| Supply chain | **Kyverno** + **Cosign** | Only signed images admitted to `qvs-app` |

---

## Repository layout

```text
app/                         # The HTTP API we run (Go) + Dockerfile
charts/api/                  # Helm chart that deploys the API (Deployment, Service,
                             #   Ingress, HPA, ServiceMonitor, PrometheusRule)
gitops/
├── bootstrap/               # Root Argo CD Application (app-of-apps entry point)
└── apps/                    # Child Applications, ordered by sync-wave:
                             #   -2 sealed-secrets, sealed-secret, kyverno, monitoring
                             #   -1 postgresql
                             #    0 network-policies, supply-chain-policies
                             #    1 api
network-policies/            # default-deny + explicit allows (see WRITEUP §Networking)
secrets/                     # SealedSecret (ciphertext only — safe in a public repo)
values/                      # Helm values for the upstream charts
supply-chain/                # Kyverno ClusterPolicy + Cosign public key
docs/                        # README (this file) + WRITEUP
```

Each child Application maps to one directory or one upstream chart, so the Argo CD
app tree reads 1:1 with the repo. Sync-waves guarantee ordering — the Sealed
Secret and its controller exist before Postgres needs the credential, and the API
(`wave 1`) starts only after the DB, network policies, and image-verification
policy are in place.

---

## Prerequisites

Docker (or OrbStack), `minikube`, `kubectl`, `helm`, `git`, `cosign`, and a GitHub
PAT with `write:packages` (to publish + sign the image). `kubeseal` only if you
re-seal credentials from scratch.

---

## Deploy from scratch

```bash
# 1. Cluster with a NetworkPolicy-enforcing CNI (see CNI ADR in the writeup)
minikube start --nodes=2 --cni=calico
minikube addons enable ingress          # in-cluster nginx — no cloud LB
minikube addons enable metrics-server   # feeds the HPA

# 2. Build, publish, and SIGN the API image (see "Supply chain" below).
#    The image is pulled from GHCR by digest — no `minikube image load` needed.

# 3. Install Argo CD (bootstrapping the controller by hand is allowed —
#    only what comes after must be GitOps-managed)
kubectl create namespace argocd
kubectl apply --server-side --force-conflicts -n argocd \
  -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

# 4. Hand the cluster to GitOps — the root app deploys everything else
kubectl apply -f gitops/bootstrap/root-application.yaml
```

### Provide the database credential (Sealed Secrets)

The committed `SealedSecret` can only be decrypted by the controller key it was
sealed against. On a **brand-new** cluster the controller mints a fresh key, so
make the matching private key available — either **restore an existing key** (no
git change, no re-seal) or **re-seal** fresh credentials:

```bash
# Restore (recommended if the old cluster still exists):
#   old cluster:
kubectl get secret -n kube-system -l sealedsecrets.bitnami.com/sealed-secrets-key -o yaml > key.yaml
#   new cluster (move key.yaml securely first):
kubectl apply -f key.yaml     # controller loads it on next start

# OR re-seal against this cluster's controller (needs kubeseal):
kubectl create secret generic qvs-postgresql-credentials -n qvs-app \
  --from-literal=password='<db-pass>' --from-literal=postgres-password='<admin-pass>' \
  --dry-run=client -o yaml \
| kubeseal --controller-name sealed-secrets-controller --controller-namespace kube-system \
    --format yaml > secrets/postgresql-sealedsecret.yaml
git commit -am "reseal DB credentials" && git push
```

> ⚠️ The exported key file is the **master decryption key** — never commit it
> (`.gitignore`d) and delete it after import. See the runbook in the writeup.

### Supply chain — publish + sign the image

Cosign signatures live in a registry next to the image, so the API is published to
GHCR and signed there; Kyverno then admits only signed images into `qvs-app`.

```bash
echo "$GHCR_PAT" | docker login ghcr.io -u <user> --password-stdin
docker build -t ghcr.io/<user>/qvs-api:0.1.0 ./app
docker push  ghcr.io/<user>/qvs-api:0.1.0
DIGEST=$(docker inspect --format='{{index .RepoDigests 0}}' ghcr.io/<user>/qvs-api:0.1.0)
cosign sign --key cosign.key "$DIGEST"     # signature attaches to the digest
# make the GHCR package public so the cluster can pull + verify anonymously
```

The chart references the image **by digest** (`image.digest`), and the Kyverno
`ClusterPolicy` in `supply-chain/` verifies the Cosign signature (public key in
[supply-chain/keys/cosign.pub](../supply-chain/keys/cosign.pub)) in `Enforce`
mode before any pod in `qvs-app` starts.

### Reach the ingress (Docker driver)

On the Docker driver, minikube's ingress is published on `127.0.0.1` **only while
`minikube tunnel` runs** — minikube standing in for a real load balancer.

```bash
minikube tunnel                                          # separate terminal; leave running
sudo sh -c 'sed -i "" "/api.qvs.local/d" /etc/hosts; echo "127.0.0.1 api.qvs.local" >> /etc/hosts'
```

---

## Verify it works

```bash
kubectl get pods,svc,ingress,netpol -n qvs-app
kubectl get applications -n argocd                       # the GitOps app tree

curl http://api.qvs.local/            # -> 200  hello from qvs-api
curl http://api.qvs.local/healthz     # -> 200  ok   (DB reachable via SELECT 1)
```

**NetworkPolicy blocks traffic** (least-privilege proof — a non-`api` pod resolves
DNS but is denied both the internet and the DB):
```bash
kubectl run netpol-test --image=busybox:1.36 -n qvs-app --restart=Never --command -- sleep 3600
kubectl exec -n qvs-app netpol-test -- nslookup qoves-postgresql.qvs-app.svc.cluster.local   # resolves
kubectl exec -n qvs-app netpol-test -- timeout 5 wget -qO- http://example.com; echo $?        # timeout (blocked)
kubectl exec -n qvs-app netpol-test -- timeout 5 nc -zv qoves-postgresql 5432; echo $?         # timeout (blocked)
kubectl delete pod netpol-test -n qvs-app
```

**Data survives a restart** (storage proof):
```bash
kubectl exec -n qvs-app qoves-postgresql-0 -c postgresql -- \
  psql -U qvs -d qvs -c "CREATE TABLE t(x int); INSERT INTO t VALUES(1);"
kubectl delete pod qoves-postgresql-0 -n qvs-app          # recreated, same PVC
kubectl exec -n qvs-app qoves-postgresql-0 -c postgresql -- \
  psql -U qvs -d qvs -c "SELECT * FROM t;"                # row still there
```

**Only signed images run** (supply-chain proof — an unsigned image is rejected):
```bash
docker tag ghcr.io/<user>/qvs-api:0.1.0 ghcr.io/<user>/qvs-api:unsigned
docker push ghcr.io/<user>/qvs-api:unsigned               # NOT signed
kubectl run supplychain-test -n qvs-app --restart=Never --image=ghcr.io/<user>/qvs-api:unsigned
#   -> admission webhook "verify-qvs-signed-images" denied: no matching signatures
```

**Metrics + alert** (observability proof):
```bash
kubectl get servicemonitor,prometheusrule -n qvs-app       # both present
kubectl -n monitoring port-forward svc/qvs-monitoring-kube-promet-prometheus 9090
#   http://localhost:9090 → query: http_requests_total{namespace="qvs-app"}
#   Status → Rules → APIHealthzErrorRate
```

---

## GitOps workflow — how to make a change

Edit a manifest or Helm value in git → `git commit && git push`. Argo CD detects
drift and reconciles: `selfHeal` re-applies desired state, `prune` removes what you
deleted. No `kubectl apply`/`edit` on workloads. To promote a new image, sign a new
digest and update `image.digest` in `charts/api/values.yaml` via git.

---

## Security summary

- **Network:** namespace is default-deny (ingress **and** egress); only DNS,
  ingress→API, API→DB, and monitoring→targets are allowed back.
- **Secrets:** credentials are encrypted (Sealed Secrets); plaintext exists only
  in-cluster and briefly at seal time. Nothing sensitive is in git.
- **Workload hardening:** non-root (UID 65532), read-only root filesystem, all
  capabilities dropped, `seccompProfile: RuntimeDefault`, no privilege escalation.
- **Supply chain:** images pinned by digest and Cosign-signed; Kyverno rejects
  unsigned images at admission (`Enforce`).
