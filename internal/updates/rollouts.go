package updates

// Rollouts and maintenance windows (docs/plan/14-v0.4.md, "Rollouts" and
// "Maintenance windows"): the shapes wave 25 fixes, kept in rollouts and
// maintenance_windows (migration 00024). The planner, the scheduler and
// their routes come in wave 26; nothing reads or writes these yet.

import "time"

// ActRollout is the action that runs a rollout: "Update all", or a
// selection, as one plan whose steps each update one container or stack, in
// depends_on order, with a snapshot first where one is possible. Its tier is
// the highest of its steps'. Not registered yet: the engine needs its
// Resolve and Execute, which come with the planner in wave 26.
const ActRollout = "updates.rollout"

// RolloutState is where a rollout is.
type RolloutState string

// The rollout states, as rollouts.state allows them.
const (
	// RolloutScheduled waits for its time or its window.
	RolloutScheduled RolloutState = "scheduled"
	// RolloutWaiting was scheduled, but its plan changed since: it waits
	// for a person to look again.
	RolloutWaiting RolloutState = "waiting"
	RolloutRunning RolloutState = "running"
	// RolloutSucceeded ran every step.
	RolloutSucceeded RolloutState = "succeeded"
	// RolloutFailed stopped at the first step that failed: that step rolled
	// back and the rest didn't start.
	RolloutFailed    RolloutState = "failed"
	RolloutCancelled RolloutState = "cancelled"
)

// StepState is where one step of a rollout is.
type StepState string

// The step states.
const (
	StepPending    StepState = "pending"
	StepRunning    StepState = "running"
	StepSucceeded  StepState = "succeeded"
	StepRolledBack StepState = "rolled_back" // it failed and rolled back
	StepNotStarted StepState = "not_started" // an earlier step failed
	StepSkipped    StepState = "skipped"     // held or snoozed: left out and listed
)

// The snapshots a step takes first.
const (
	SnapshotNone = ""    // none possible; the plan says so
	SnapshotGit  = "git" // a commit of an owned stack's files
	SnapshotPVE  = "pve" // a PVE snapshot of the guest the container runs on
)

// RolloutStep is one container or stack in a rollout, as rollouts.steps
// holds it.
type RolloutStep struct {
	Target   string    `json:"target"`             // the container's or stack's resource ID
	Action   string    `json:"action"`             // the action that updates it
	Snapshot string    `json:"snapshot,omitempty"` // SnapshotGit, SnapshotPVE or none
	State    StepState `json:"state"`
	Reason   string    `json:"reason,omitempty"` // why it was skipped or failed, a sentence
}

// Rollout is one row of rollouts.
type Rollout struct {
	ID           string        `json:"id"` // rol_…
	CreatedBy    string        `json:"created_by,omitempty"`
	PlanHash     string        `json:"plan_hash"`
	State        RolloutState  `json:"state"`
	WindowID     string        `json:"window_id,omitempty"`
	ScheduledFor time.Time     `json:"scheduled_for,omitzero"` // zero: at once, or when its window opens
	Steps        []RolloutStep `json:"steps"`
	CreatedAt    time.Time     `json:"created_at"`
	StartedAt    time.Time     `json:"started_at,omitzero"`
	FinishedAt   time.Time     `json:"finished_at,omitzero"`
}

// MaxWeeklyWindow is the longest a weekly window may be.
const MaxWeeklyWindow = 7 * 24 * time.Hour

// MaintenanceWindow is one row of maintenance_windows: a time range for a
// set of hosts, once or every week. During it, incidents on its hosts open
// as expected and don't notify; one still open when it ends notifies then.
type MaintenanceWindow struct {
	ID        string    `json:"id"` // mw_…
	Name      string    `json:"name"`
	Hosts     []string  `json:"hosts"` // resource IDs
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Weekly    bool      `json:"weekly"` // repeats StartsAt..EndsAt every 7 days
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
