# Deploying leutik-tarik on Kubernetes

Everything runs in the `leutik-system` namespace: a Deployment + ClusterIP
Service for the platform, and a StatefulSet + PVC for ClickHouse. Nothing is
published on the node — the only way in is the Caddy ingress block for
`leutik-tarik.komuterku.com` in the `jatelindo-api-gateway` repo:

```
@leutik host leutik-tarik.komuterku.com
handle @leutik {
  reverse_proxy leutik-tarik.leutik-system.svc.cluster.local:8080 { ... }
}
```

## Files

| File | What |
|---|---|
| `00-namespace.yaml` | `leutik-system` |
| `10-clickhouse.yaml` | StatefulSet + ClusterIP Service + 20Gi PVC |
| `20-leutik-tarik.yaml` | ConfigMap (retention policy), 10Gi profiles PVC, Service, Deployment |

## Apply

```sh
kubectl apply -f deploy/k8s/00-namespace.yaml
kubectl apply -f deploy/k8s/10-clickhouse.yaml
kubectl rollout status statefulset/clickhouse -n leutik-system
kubectl apply -f deploy/k8s/20-leutik-tarik.yaml   # replace IMAGE_PLACEHOLDER first
```

## Image

The build runs wherever a **Go 1.26+** toolchain exists (the module needs it;
the box's host Go is 1.22), producing a static binary that the Dockerfile copies
onto distroless:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o leutik-tarik ./cmd/leutik-tarik
docker build -f deploy/Dockerfile -t <registry>/jatelindo/leutik-tarik:<tag> .
```

The local registry on the Jupler box (`127.0.0.1:5000`) is htpasswd-protected,
so pushing needs credentials from the CI configuration. Without them the image
can be imported directly into containerd instead:

```sh
docker save 127.0.0.1:5000/jatelindo/leutik-tarik:dev-1 | ctr -n k8s.io images import -
```

With that, `imagePullPolicy: IfNotPresent` finds it locally. Note the trade-off:
the image then exists only on this node, so a registry push is the right answer
before the platform moves anywhere else.

## Why these choices

- **initContainer runs `-migrate`.** A rolling update against a database with a
  missing column should fail the rollout, not serve 500s.
- **`replicas: 1`, `strategy: Recreate`.** Ingestion writes and the log ring are
  per-process; a second replica would split the ring. Scaling out needs the ring
  in a shared store first.
- **`readOnlyRootFilesystem`, non-root, all capabilities dropped.** The pod
  writes only to the profiles PVC and an `emptyDir` `/tmp`.
- **`CLICKHOUSE_SKIP_USER_SETUP=1`.** Dev cluster only, reachable solely on the
  ClusterIP Service. A production install must set a password.

## Reset dev data

During development the stored traces are disposable. This wipes spans, log
snapshots, profile metadata **and** the profile blobs, while keeping the schema
(`schema_migrations` is left alone, so the next start does not re-run DDL):

```sh
export KUBECONFIG=/etc/kubernetes/admin.conf
CH=$(kubectl -n leutik-system get pods -l app.kubernetes.io/name=clickhouse -o jsonpath='{.items[0].metadata.name}')

for t in spans log_snapshots profile_objects; do
  kubectl -n leutik-system exec "$CH" -- clickhouse-client --query "TRUNCATE TABLE IF EXISTS $t"
done

# profile blobs live on the PVC, and the app image is distroless (no shell),
# so mount the volume in a throwaway pod to clear it
kubectl -n leutik-system run leutik-cleanup --rm -i --restart=Never --image=busybox:1.36 \
  --overrides='{"spec":{"volumes":[{"name":"p","persistentVolumeClaim":{"claimName":"leutik-profiles"}}],
  "containers":[{"name":"leutik-cleanup","image":"busybox:1.36",
  "command":["sh","-c","rm -rf /data/* /data/.[!.]* 2>/dev/null; echo cleared"],
  "volumeMounts":[{"name":"p","mountPath":"/data"}]}]}}'
```

Every page has an explicit empty state (`No operations in this window`,
`Nothing worth checking`, `No profiles yet`), so a wipe is visible rather than
looking like a broken query.
