#!/bin/bash
# Roll out leutik-tarik on the Jupler node without any registry.
#
# The image is built here from the binary CI just copied over, imported straight
# into containerd, and referenced by the Deployment. The local registry is
# htpasswd-protected and needs a credential we do not have in CI, so importing
# into containerd is the registry-free path. It is single-node only: moving the
# pod elsewhere requires a real registry push.
set -euo pipefail

TAG="${IMAGE_TAG:?IMAGE_TAG is required, e.g. sha-1a2b3c4}"
IMG="127.0.0.1:5000/jatelindo/leutik-tarik:${TAG}"
DIR=/opt/leutik-tarik

cd "$DIR"
echo "== building ${IMG} =="
docker build -t "$IMG" . >/dev/null

echo "== importing into containerd =="
docker save "$IMG" | ctr -n k8s.io images import - >/dev/null

export KUBECONFIG=/etc/kubernetes/admin.conf

echo "== applying manifests =="
# The manifests are applied, not just the image: a deploy that only sets the
# image silently ignores spec changes (env vars, probes, resources). That bug
# hid LEUTIK_CAPTURE_SQL_TEXT for a whole rollout before it was noticed.
for f in k8s/00-namespace.yaml k8s/10-clickhouse.yaml k8s/20-leutik-tarik.yaml; do
  [ -f "$f" ] || { echo "missing $f"; exit 1; }
  sed "s|IMAGE_PLACEHOLDER|$IMG|g" "$f" | kubectl apply -f - >/dev/null
done

echo "== rolling out =="
# Both the init container (migrations) and the app must move together: a new app
# against an old schema is exactly what the initContainer exists to prevent.
kubectl -n leutik-system set image deployment/leutik-tarik \
  leutik-tarik="$IMG" migrate="$IMG"
kubectl -n leutik-system rollout status deployment/leutik-tarik --timeout=180s

echo "== live: =="
kubectl -n leutik-system get pods -l app.kubernetes.io/name=leutik-tarik --no-headers

# Keep the node from filling with old imports: drop tags this deployment no
# longer references (docker) and the matching containerd images.
CURRENT=$(kubectl -n leutik-system get deploy leutik-tarik \
  -o jsonpath='{.spec.template.spec.containers[0].image}')
for old in $(docker images 127.0.0.1:5000/jatelindo/leutik-tarik --format '{{.Repository}}:{{.Tag}}'); do
  [ "$old" = "$CURRENT" ] && continue
  docker rmi "$old" >/dev/null 2>&1 || true
done
echo "== done: ${IMG} =="
