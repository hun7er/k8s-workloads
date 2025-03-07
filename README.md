# Kubernetes Workloads Viewer

A modern, real-time web interface for monitoring Kubernetes workloads across your clusters. This application provides a user-friendly dashboard to view deployments, stateful sets and pods with their current status, logs, and events.

<img width="1275" alt="Screenshot" src="https://github.com/user-attachments/assets/a5c8adec-062c-4b9b-826f-95710f4eb86d" />

## Quick Start

### Running Locally

1. Clone the repository:

```bash
git clone [repository-url]
cd k8s-workloads
```

2. Install dependencies:

```bash
go mod download
```

3. Run the application:

```bash
go run main.go
```

The application will be available at `http://localhost:32443`

### Running in Kubernetes

1. Create the namespace:

```bash
kubectl create namespace k8s-workloads
```

2. Apply the Kubernetes manifests:

```bash
kubectl apply -f kubernetes.yaml
```

## Configuration

The application uses the following configuration:

- Default port: 32443
- Auto-refresh interval: 30 seconds
- Kubernetes configuration: Uses in-cluster config when deployed in Kubernetes, or local kubeconfig when running outside
