#!/usr/bin/env bash
# POC runner: Grove's kueue backend with Workload-Aware Scheduling (WAS) objects. See README.md.
set -euo pipefail
cd "$(dirname "$0")"

GROVE_NS=grove-system
WAIT=${WAIT:-45}
PROFILES_PREFIX='[{"name":"default-scheduler"},{"name":"kai-scheduler"},{"name":"kueue"'

usage() {
  cat <<'EOF'
usage: run.sh setup | mode <Off|GangOnly|Full|WASTopology> | run <scenario> | show <scenario> | clean <scenario> | all
scenarios: agree split atomic was-places was-cannot (files in scenarios/)
EOF
  exit 1
}

nodes() {
  kubectl get nodes -l poc.grove.io/set -L kubernetes.io/rack,poc.grove.io/set
}

setup() {
  kubectl apply -f setup.yaml
  kubectl wait --for=condition=Ready node -l poc.grove.io/set --timeout=2m
  # Grove creates the Kueue Topology named after the ClusterTopologyBinding.
  for _ in $(seq 60); do
    kubectl get topologies.kueue.x-k8s.io poc-topology >/dev/null 2>&1 && break
    sleep 2
  done
  kubectl wait --for=condition=Active clusterqueues.kueue.x-k8s.io poc-tas poc-tas-atomic poc-quota --timeout=2m
  nodes
}

mode() {
  local profiles
  case "${1:-}" in
    Off) profiles="${PROFILES_PREFIX}}]" ;;
    GangOnly | Full | WASTopology) profiles="${PROFILES_PREFIX},\"config\":{\"wasMode\":\"$1\"}}]" ;;
    *) usage ;;
  esac
  echo "### wasMode=$1"
  helm upgrade grove-operator ../../charts -n "$GROVE_NS" --reuse-values --set-json "config.scheduler.profiles=$profiles" >/dev/null
  kubectl -n "$GROVE_NS" rollout restart deployment >/dev/null
  kubectl -n "$GROVE_NS" rollout status deployment --timeout=5m
}

show() {
  local pcs="poc-$1" gang="poc-$1-0"
  echo "--- Kueue Workload $gang"
  kubectl -n default get workloads.kueue.x-k8s.io "$gang" \
    -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}' || true
  kubectl -n default get events --field-selector "involvedObject.kind=Workload,involvedObject.name=$gang" \
    --sort-by=.lastTimestamp -o custom-columns='COUNT:.count,REASON:.reason,MESSAGE:.message' || true
  echo "--- Pods (KUEUE_PIN = node Kueue's TAS chose, NODE = node kube-scheduler bound)"
  kubectl -n default get pods -l "app.kubernetes.io/part-of=$pcs" -o custom-columns='POD:.metadata.name,KUEUE_PIN:.spec.nodeSelector.kubernetes\.io/hostname,NODE:.spec.nodeName,PHASE:.status.phase,GATES:.spec.schedulingGates[*].name,PODGROUP:.spec.schedulingGroup.podGroupName'
  echo "--- WAS objects"
  kubectl -n default get compositepodgroups.scheduling.k8s.io -l "grove.io/podgang=$gang" \
    -o custom-columns='COMPOSITE:.metadata.name,MIN_GROUPS:.spec.schedulingPolicy.gang.minGroupCount,TOPOLOGY:.spec.schedulingConstraints.topology[*].key,CONDITIONS:.status.conditions[*].type,STATUS:.status.conditions[*].status,REASON:.status.conditions[*].reason' 2>/dev/null || true
  kubectl -n default get podgroups.scheduling.k8s.io -l "grove.io/podgang=$gang" \
    -o custom-columns='PODGROUP:.metadata.name,CLIQUE:.metadata.labels.grove\.io/podclique,MIN:.spec.schedulingPolicy.gang.minCount,TOPOLOGY:.spec.schedulingConstraints.topology[*].key,SCHEDULED:.status.conditions[?(@.type=="PodGroupInitiallyScheduled")].status,REASON:.status.conditions[?(@.type=="PodGroupInitiallyScheduled")].reason' 2>/dev/null || true
  echo "--- Latest FailedScheduling events"
  kubectl -n default get events --field-selector reason=FailedScheduling --sort-by=.lastTimestamp \
    -o custom-columns='POD:.involvedObject.name,MESSAGE:.message' | grep "^$pcs" | tail -4 || true
}

run() {
  [ -f "scenarios/$1.yaml" ] || usage
  echo "### scenario $1"
  kubectl apply -f "scenarios/$1.yaml"
  sleep "$WAIT"
  show "$1"
}

clean() {
  [ -f "scenarios/$1.yaml" ] || usage
  kubectl delete -f "scenarios/$1.yaml" --ignore-not-found --wait=true --timeout=2m
  kubectl -n default wait --for=delete pod -l "app.kubernetes.io/part-of=poc-$1" --timeout=2m || true
}

all() {
  local log
  log="results-$(date +%Y%m%d-%H%M%S).log"
  {
    nodes
    mode Off
    run atomic && clean atomic
    run split && clean split
    mode GangOnly
    run atomic && clean atomic
    run split && clean split
    mode Full
    run split && clean split
    run agree && clean agree
    mode WASTopology
    run was-places && clean was-places
    run was-cannot && clean was-cannot
  } 2>&1 | tee "$log"
  echo "results saved to $(pwd)/$log"
}

case "${1:-}" in
  setup | all) "$1" ;;
  mode | run | show | clean) [ $# -eq 2 ] || usage; "$1" "$2" ;;
  *) usage ;;
esac
