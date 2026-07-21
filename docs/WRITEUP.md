# GitOps Deployment Write-up

## 1. Run It

### Prerequisites

- Docker
- Minikube
- kubectl
- Helm
- Argo CD CLI (optional)

### Repository Layout

```text
gitops/
├── bootstrap/              # Root Argo CD Application
├── apps/                   # Child Applications
network-policies/           # Kubernetes NetworkPolicies
secrets/                    # Sealed Secrets
values/                     # Helm values
supply-chain/               # Kyverno policies
docs/                       # Documentation
```

### Deploy From Scratch

1. Start Minikube.

```bash
minikube start
```

2. Install Argo CD.

```bash
kubectl create namespace argocd

kubectl apply \
  --server-side \
  --force-conflicts \
  -n argocd \
  -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
```

3. Bootstrap GitOps.

```bash
kubectl apply -f gitops/bootstrap/root-application.yaml
```

The root application deploys all child applications automatically, including PostgreSQL, Sealed Secrets, NetworkPolicies, monitoring, and supply-chain policies.

### GitOps Workflow

Infrastructure changes are made by modifying manifests or Helm values in Git, committing the changes, and pushing them to the repository. Argo CD continuously monitors the repository and reconciles the cluster to the desired state without requiring manual deployment.

---

# 2. Architecture Decisions (ADR)

## CNI

**Decision**

Use Calico as the Container Network Interface.

**Alternatives**

- Cilium
- Flannel

**Reason**

Calico provides mature NetworkPolicy support and integrates well with Kubernetes. It satisfies the assignment requirements while remaining lightweight for Minikube.

---

## Secret Management

**Decision**

Use Bitnami Sealed Secrets.

**Alternatives**

- HashiCorp Vault
- External Secrets Operator
- Kubernetes Secrets only

**Reason**

Sealed Secrets allows encrypted secrets to be committed safely to Git while remaining simple to operate in a GitOps workflow.

---

## PostgreSQL Deployment

**Decision**

Deploy PostgreSQL using the Bitnami Helm chart.

**Alternatives**

- CloudNativePG Operator
- Crunchy PostgreSQL Operator
- Raw Kubernetes manifests

**Reason**

The assignment requires a single PostgreSQL instance. The Bitnami Helm chart provides a well-maintained, production-ready deployment with persistence, metrics, and configurable resource management without introducing the operational complexity of a database operator.

---

## Scaling Strategy

**Decision**

Configure Horizontal Pod Autoscaling using CPU utilisation.

**Alternatives**

- Memory utilisation
- Custom Prometheus metrics
- KEDA

**Reason**

CPU utilisation is a simple and widely used scaling signal supported natively by Kubernetes. It provides automatic horizontal scaling without requiring additional infrastructure.

---

# 3. What Minikube Did For Me

Minikube provides many capabilities that would need to be designed and operated explicitly in a production Kubernetes environment.

| Minikube Provides | Production Equivalent |
|-------------------|-----------------------|
| Single-node Kubernetes cluster | Highly available multi-node cluster |
| Control-plane bootstrap | kubeadm or Cluster API |
| Built-in storage provisioner | CSI storage driver (EBS, Ceph, Longhorn, etc.) |
| Networking | Production CNI installation (Calico or Cilium) |
| Ingress support | NGINX Ingress with MetalLB or cloud load balancers |
| Embedded etcd | Highly available etcd cluster with regular backups |

In a production environment these components would require dedicated installation, monitoring, backup, upgrades, and disaster recovery planning.

---

# 4. Production Gaps

While this implementation demonstrates the required GitOps workflow, several capabilities would be required before serving production traffic.

- Highly available Kubernetes control plane.
- PostgreSQL replication and automated failover.
- Automated backup and restore for persistent volumes and databases.
- Centralised secret management using Vault or an external secret provider.
- Multi-cluster GitOps for development, staging, and production environments.
- Continuous image vulnerability scanning and enforced signature verification.
- Automated Kubernetes and application upgrade strategy.
- Comprehensive alerting, dashboards, and log aggregation.

These improvements would increase resilience, security, and operational maturity.

---

# 5. Runbook – PostgreSQL Pod Failure

## Scenario

The PostgreSQL pod becomes unavailable or enters a CrashLoopBackOff state.

## Recovery Steps

1. Verify the application status.

```bash
argocd app get qvs-postgresql
```

2. Check the PostgreSQL pod.

```bash
kubectl get pods -n qvs-app
```

3. Inspect pod logs.

```bash
kubectl logs <postgres-pod> -n qvs-app
```

4. Verify the PersistentVolumeClaim.

```bash
kubectl get pvc -n qvs-app
```

5. If the failure was caused by an incorrect configuration change, revert the Git commit.

```bash
git revert <commit>
git push
```

Argo CD automatically reconciles the cluster to the last known good configuration.

6. If the pod terminated unexpectedly but the configuration is correct, Kubernetes recreates the pod automatically.

```bash
kubectl get pods -n qvs-app
```

7. Verify database availability.

```bash
kubectl exec -it <postgres-pod> -n qvs-app -- pg_isready
```

8. Confirm that persistent data is still present after recovery.

---

## Summary

This implementation demonstrates:

- GitOps deployment using the Argo CD App of Apps pattern.
- Secure secret management with Sealed Secrets.
- Persistent PostgreSQL storage.
- Network isolation using Kubernetes NetworkPolicies.
- Resource management through requests, limits, and autoscaling.
- Monitoring integration.
- A foundation for supply-chain security using Kyverno.