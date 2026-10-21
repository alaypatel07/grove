# POC: Kueue backend with Workload-Aware Scheduling objects

The kueue backend can also build each PodGang's WAS hierarchy (Workload, CompositePodGroup, PodGroups),
reusing the default-scheduler backend from GREP-531, and point each Pod's `spec.schedulingGroup` at its
clique's PodGroup. The `wasMode` field of the kueue scheduler profile selects who enforces topology:

| wasMode | Kueue Workload | WAS hierarchy |
|---|---|---|
| unset (Off) | topology request per PodSet | none |
| `GangOnly` | topology request per PodSet | gangs, no topology |
| `Full` | topology request per PodSet | gangs and topology |
| `WASTopology` | no topology request (use a non-TAS queue) | gangs and topology |

## Cluster

WAS needs Kubernetes 1.37 with alpha feature gates set when the cluster is created. Use Helm 3: Helm 4's
server-side apply conflicts with the operator over the webhook cert Secret. From `operator/`:

```bash
export E2E_SCHEDULER__KUEUE__ENABLED=true E2E_SCHEDULER__KAI__ENABLED=false
export E2E_CLUSTER__WAS_ENABLED=true E2E_CLUSTER__WORKER_NODES=1 E2E_KWOK__NODES=2
export E2E_CLUSTER__WORKER_MEMORY=1g
make e2e-cluster-up
```

This replaces any existing `shared-e2e-test-cluster`. With both Kueue and WAS enabled, setup registers the
kueue profile with `wasMode: GangOnly` and no kai-scheduler profile. The POC schedules nothing on the k3d
agents, so one is enough. It also gets 1g instead of the default 150m, which can OOM-kill k3s at startup.
If an agent still crash-loops, check `sysctl fs.inotify.max_user_instances`; 128 is too low.

## Run

```bash
./hack/poc-kueue-was/run.sh setup   # KWOK racks, topology, flavors and queues
./hack/poc-kueue-was/run.sh all     # every scenario; output also goes to results-<time>.log
```

Or step by step: `run.sh mode Full`, `run.sh run split`, `run.sh show split`, `run.sh clean split`.
Clean a scenario before changing modes, because a PodGang's WAS hierarchy can't change in place.
Each scenario file says what it tests and what to expect.
