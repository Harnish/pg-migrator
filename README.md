# pg-migrator

Small CLI tool to run PostgreSQL migrations/dumps between two databases. Entrypoint: [`main.main`](main.go). Core logic lives in [`Migrator.Migrate`](main.go) and the dump/restore implementation is in [`Migrator.DumpDatabase`](main.go). Relevant files: [main.go](main.go), [Dockerfile](Dockerfile), [go.mod](go.mod), [pg-migrator](pg-migrator), [kubernetes/job.yaml](kubernetes/job.yaml), [kubernetes/configmap.yaml](kubernetes/configmap.yaml), [kubernetes/secret.yaml](kubernetes/secret.yaml).

Prerequisites
- Go (to build locally) or Docker (to run container)
- kubectl + access to a cluster (for Kubernetes)
- If running the built binary locally, ensure `pg_dump` and `pg_restore` are available in PATH (the official Docker image includes them).

Quick CLI (local) build and run
1. Build the binary:
```bash
go build -o pg-migrator main.go
```

2. Run the CLI. Required flags are shown in the binary (see [`main.main`](main.go)). Example:
```bash
./pg-migrator \
  -src-host=SRC_HOST -src-port=5432 -src-user=SRC_USER -src-password=SRC_PASSWORD \
  -dst-host=DST_HOST -dst-port=5432 -dst-user=DST_USER -dst-password=DST_PASSWORD \
  -dump-dir=/tmp/pg_migration
```

Flags available (defined in [`main.go`](main.go)):
- -src-host, -src-port, -src-user, -src-password
- -dst-host, -dst-port, -dst-user, -dst-password
- -dump-dir

Note: the binary uses external commands (`pg_dump` / `pg_restore`) via exec (see [`Migrator.DumpDatabase`](main.go)), so they must be present when running locally.

Build Docker image
The repository includes a multi-stage Dockerfile that builds a static Go binary and uses the official Postgres image (so `pg_dump`/`pg_restore` are available).

Build:
```bash
docker build -t registry.whiskeyonthe.rocks/pg-migrator/pg-migrator:latest .
```
Dockerfile: [Dockerfile](Dockerfile)

Run container locally (example):
```bash
docker run --rm \
  -e SRC_HOST=... -e SRC_PORT=5432 -e SRC_USER=... -e SRC_PASSWORD=... \
  -e DST_HOST=... -e DST_PORT=5432 -e DST_USER=... -e DST_PASSWORD=... \
  registry.whiskeyonthe.rocks/pg-migrator/pg-migrator:latest \
  -dump-dir=/tmp/pg_migration
```

Push to registry:
```bash
docker push registry.whiskeyonthe.rocks/pg-migrator/pg-migrator:latest
```

Kubernetes usage
Manifests are provided under [kubernetes/](kubernetes/). See:
- [kubernetes/configmap.yaml](kubernetes/configmap.yaml)
- [kubernetes/secret.yaml](kubernetes/secret.yaml)
- [kubernetes/job.yaml](kubernetes/job.yaml)

The Job mounts an emptyDir at `/tmp/pg_migration` and passes flags from environment variables. To deploy:

1. Edit `kubernetes/configmap.yaml` and `kubernetes/secret.yaml` for your environment (or create equivalents).
2. Update the image tag in [kubernetes/job.yaml](kubernetes/job.yaml) if you built a custom image.
3. Apply manifests:
```bash
kubectl apply -f kubernetes/configmap.yaml
kubectl apply -f kubernetes/secret.yaml
kubectl apply -f kubernetes/job.yaml
```
4. Watch job and fetch logs:
```bash
kubectl get jobs -n default
kubectl logs job/pg-migration-job -n default
```

Important details from the manifests
- The Job disables retries: `backoffLimit: 0` and uses `ttlSecondsAfterFinished: 86400` to clean up after 24 hours (see [kubernetes/job.yaml](kubernetes/job.yaml)).
- The Job sets env vars from the ConfigMap and Secret (see [kubernetes/job.yaml](kubernetes/job.yaml), [kubernetes/configmap.yaml](kubernetes/configmap.yaml), [kubernetes/secret.yaml](kubernetes/secret.yaml)).
- Dumps are written to an emptyDir with a `10Gi` size limit (see [kubernetes/job.yaml](kubernetes/job.yaml)).

Troubleshooting
- If running locally and you see pg_dump/pg_restore errors, install the Postgres client tools or run the provided Docker image which includes them.
- If the Job cannot pull the image, verify the image name and registry credentials.
- Logs from the binary include success/failure messages from `Migrator.MigrateDatabases` (see [`Migrator.MigrateDatabases`](main.go)).

References
- Entrypoint: [`main.main`](main.go) — [main.go](main.go)
- Dump logic: [`Migrator.DumpDatabase`](main.go) — [main.go](main.go)
- Migration orchestration: [`Migrator.Migrate`](main.go) / [`Migrator.MigrateDatabases`](main.go) — [main.go](main.go)
- Dockerfile: [Dockerfile](Dockerfile)
- Go module: [go.mod](go.mod)
- Kubernetes manifests: [kubernetes/job.yaml](kubernetes/job.yaml), [kubernetes/configmap.yaml](kubernetes/configmap.yaml), [kubernetes/secret.yaml](kubernetes/secret.yaml)
- Prebuilt binary in repo: [pg-migrator](pg-migrator)