# uptime-kuma Helm chart

Deploys the Go backend, `kuma-analytics`, with the same stack as `server-go/compose.yaml`:

| Component      | Kind        | Default | Notes                                                         |
| -------------- | ----------- | ------- | ------------------------------------------------------------- |
| analytics      | Deployment  | on      | API and web UI on port 3002. Always 1 replica, `Recreate`     |
| timescaledb    | StatefulSet | on      | Off: set `externalDatabase.url` or `DATABASE_URL` in a secret |
| otel-collector | Deployment  | on      | Sends traces to Tempo and metrics to Prometheus, or logs them |
| prometheus     | StatefulSet | off     |                                                               |
| tempo          | StatefulSet | on      |                                                               |
| grafana        | Deployment  | off     | Dashboards from `server-go/observability`, needs prometheus   |

`dashboards/` is a symlink to `../../observability/grafana/dashboards`, so the chart and Compose
share one copy. Helm follows it; package with `helm package` if a tool can't.

## Image

`.github/workflows/docker-release.yml` publishes `ghcr.io/pantheon-lab/uptime-kuma` for amd64 and arm64:

| Event            | Tags                         |
| ---------------- | ---------------------------- |
| Push to `main`   | `main`, `sha-<short commit>` |
| Git tag `v1.2.3` | `1.2.3`, `1.2`, `latest`     |

The chart's default tag is its `appVersion`, so a release needs the matching `v<appVersion>` git tag. Pre-release
versions such as `3.0.0-beta.0` get only their exact tag, not `latest`.

It also pushes to Docker Hub when the repository variable `DOCKERHUB_IMAGE` (such as `pantheonlab/uptime-kuma`)
and the secrets `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` are set.

## Install

```bash
helm upgrade --install uptime-kuma server-go/charts/uptime-kuma -n uptime-kuma --create-namespace
```

## Secrets

With `existingSecret` empty, the chart creates `<release>-uptime-kuma-secrets` (or `<release>-secrets` when the release
name contains `uptime-kuma`) with random values, and reuses them on `helm upgrade`. The Secret is kept on
`helm uninstall` because the database volume still needs its password.

Argo CD renders without cluster access, so it would generate new values on every sync. Create the secret yourself
and set `existingSecret` there. Keys: `ANALYTICS_API_TOKEN` (at least 32 characters), `POSTGRES_PASSWORD` (with
the bundled TimescaleDB, URL-safe), `DATABASE_URL` (with an external database), `GRAFANA_DB_PASSWORD` and
`GRAFANA_ADMIN_PASSWORD` (with Grafana).

Read the admin token:

```bash
kubectl -n uptime-kuma get secret uptime-kuma-secrets -o jsonpath='{.data.ANALYTICS_API_TOKEN}' | base64 -d
```

## Importing from MariaDB

`import.enabled` runs `kuma-import` as a Job against the chart's database. Put the source DSN in a secret first:

```bash
kubectl -n uptime-kuma create secret generic kuma-import \
  --from-literal=SOURCE_MARIADB_DSN='kuma_import:password@tcp(host:3306)/kuma'

# Dry run, then read the JSON report
helm upgrade uptime-kuma server-go/charts/uptime-kuma -n uptime-kuma --reuse-values \
  --set import.enabled=true --set import.dsnSecret=kuma-import
kubectl -n uptime-kuma logs -l app.kubernetes.io/component=import --tail=-1

# Import, and later the cutover
helm upgrade ... --reuse-values --set-json 'import.args=["-apply"]'
helm upgrade ... --reuse-values --set-json 'import.args=["-apply","-activate"]'
kubectl -n uptime-kuma rollout restart deploy/uptime-kuma-analytics

# Done: remove the job
helm upgrade ... --reuse-values --set import.enabled=false
```

Each combination of args and image gets its own Job, which is deleted a day after it finishes.
