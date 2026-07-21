# QVS GitOps Deployment

## Overview

This repository implements a GitOps-based Kubernetes deployment using Argo CD on Minikube. The solution provisions PostgreSQL, manages secrets securely with Sealed Secrets, enforces network isolation through NetworkPolicies, and includes monitoring and autoscaling configuration where applicable.

## Architecture

- Kubernetes (Minikube)
- Argo CD (App of Apps pattern)
- Bitnami PostgreSQL Helm Chart
- Bitnami Sealed Secrets
- Kyverno (stretch goal)
- Prometheus monitoring
- Kubernetes NetworkPolicies

## Repository Structure

```text
gitops/
├── bootstrap/
├── apps/
network-policies/
secrets/
values/
supply-chain/
docs/
```

## Deployment

1. Start Minikube.

```bash
minikube start
```

2. Install Argo CD.

```bash
kubectl create namespace argocd

kubectl apply -n argocd \
-f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
```

3. Bootstrap the root application.

```bash
kubectl apply -f gitops/bootstrap/root-application.yaml
```

Argo CD automatically deploys all child applications.

## Components

| Component | Purpose |
|----------|---------|
| Argo CD | GitOps deployment |
| PostgreSQL | Stateful database |
| Sealed Secrets | Encrypted secret management |
| NetworkPolicies | Default-deny network security |
| Prometheus | Metrics collection |
| Kyverno | Supply-chain policy (stretch) |

## Security

- Default deny ingress and egress policies
- Explicit DNS and PostgreSQL communication rules
- Database credentials stored as Sealed Secrets
- Principle of least privilege through namespace isolation

## Storage

PostgreSQL uses a PersistentVolumeClaim to preserve data across pod restarts.

Data persistence was validated by:

- creating a table
- inserting data
- deleting the PostgreSQL pod
- confirming the data remained after the pod was recreated

## Monitoring

PostgreSQL metrics are enabled using the Bitnami PostgreSQL exporter.

The deployment includes configuration for Prometheus scraping and alerting.

## Resource Management

The deployment defines:

- CPU and memory requests
- CPU and memory limits
- Horizontal Pod Autoscaler configuration for application workloads

## Stretch Goal

Kyverno was deployed to begin implementing image verification policies for software supply-chain security. Due to rebuilding the Minikube environment during development, end-to-end image signature verification was not completed.

## Challenges

During development the Minikube cluster was recreated, requiring the reinstallation of Argo CD and Sealed Secrets and the recovery of GitOps-managed resources. This reinforced the importance of declarative infrastructure and GitOps for cluster recovery.

## Outcome

This implementation demonstrates:

- GitOps deployment with Argo CD
- Secure secret management
- Persistent storage
- Network isolation
- Kubernetes resource management
- Monitoring integration
- Extensible platform for supply-chain security