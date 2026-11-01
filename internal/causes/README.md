# internal/causes

Cause hints: for each resource that isn't ok, and each open problem, a sentence about what it needs that isn't ok either or changed around when the trouble began, and the resource it points at. A hint never changes health and never opens an incident. The plan is in [docs/plan/09-v0.3.md](../../docs/plan/09-v0.3.md), "Cause hints".

`Find` is the engine: a pure function of the graph, the clock, the inbox, the health history and the events the `Service` remembers. The `Service` runs it when the graph or the inbox changes (debounced 1 s) and every minute, writes the `causes` topic (`topics.CauseSet`), and adds hints to open problems. It reads only `graph`, `history` and the topics.

## Symptoms

- Every primary (a resource without an outgoing `same_as`) whose health is `down` or `attention`, merged over its members as tiles merge it. It began at its newest `health.since`, else now.
- Every target of an open `problem` that isn't a security finding. It began when the problem opened, if that's earlier.
- An integration's own resource is never a symptom: its trouble is a repair.

## The walk

Breadth first from the symptom, at most three relations, each resource once, so a cycle ends. Every step goes to a primary.

| The symptom… | needs |
|---|---|
| `runs_on`, `part_of`, `depends_on` or `stores_on` X | X |
| is a route that `routes_to` X | X |
| is a check that `monitors` X | X |
| has X that `provides_network_for` it | X |

Never up `part_of` into a compose stack: its health is its containers' worst, so it would blame a container on itself.

## Candidates

A resource on the walk is a candidate for the first of these that holds. The window runs from 30 minutes before the symptom began to 2 minutes after.

| Rule | When |
|---|---|
| `oom_killed`, `exited` | A container's `oom_killed` or `exit_code`, at `finished_at` (or `health.since` when it's exited), in the window. The `Service` remembers these for an hour, since a restarted container stops saying so |
| `restarted` | A container's `restart_count` went up since the last pass, at `started_at` |
| `started` | A container's or guest's `started_at`, or a host's `boot_at`, in the window |
| `incident` | An open problem on it that opened before the window ends |
| `state_change` | History has it going from ok to worse in the window (a real hub with history on; demo mode has none) |
| `health` | It's `down`, `attention` or `maintenance` now, since before the window ends |

## Ranking and the sentence

- **The root** comes first. It's the candidate no other candidate's walk passes through, the furthest from the symptom, then the closest in time.
- **The rest** follow, nearest first, then closest in time, up to three causes in all. A candidate on the root's path that is only "not ok now" repeats the root and is left out: qBittorrent's app is down because its container was OOM-killed.
- **The sentence** names the cause, then the path in words: "qBittorrent's container was OOM-killed at 14:03. Sonarr depends on qBittorrent." A container one app runs on is named for that app, and the path stops at the app. `args.what` and `args.why` are the two halves (a tile shows `what`); `message` is `cause.<rule>`.
- **Times** are the hub's local time of day, or the Demo Home's display clock in demo mode.

## Incidents

Open problems that aren't security findings gain the hints for their target, after their own causes and the correlator's. A hint is skipped when the incident already names its resource. What was added is remembered and taken off before the next pass, as the correlator does with its own causes. Repairs, hints and suggestions gain none.

## Tests

- `causes_test.go`: each rule on its own.
- `internal/demo/causes_test.go`: the Demo Home incident suite. It drives the day from 13:30 to 14:05 with a pass every 10 simulated seconds, then checks every resource's first cause.
