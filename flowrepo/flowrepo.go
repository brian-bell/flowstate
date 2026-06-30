// Package flowrepo defines the single shared repository contract for Flow
// records. It abstracts persistence behind FlowRepository so the filesystem
// flowstore.Store, the in-memory Fake, and future storage backends can be
// substituted without changing callers. The package depends only on flowstore
// value types; flowstore must not depend on flowrepo, so the compile assertion
// that *flowstore.Store satisfies FlowRepository lives in flowrepo tests.
package flowrepo

import "github.com/brian-bell/flowstate/flowstore"

// FlowRepository is the persistence surface for Flow records. It captures the
// read, list, and every mutating operation the filesystem store exposes, so
// alternate repositories preserve the same derived-state and validation rules.
type FlowRepository interface {
	Read(flowID string) (flowstore.FlowRecord, error)
	List(filter flowstore.FlowFilter) ([]flowstore.FlowRecord, error)
	Create(record flowstore.FlowRecord) (flowstore.FlowRecord, error)
	SetPhase(update flowstore.PhaseUpdate) (flowstore.FlowRecord, error)
	RestartPhase(update flowstore.PhaseRestartUpdate) (flowstore.FlowRecord, error)
	AddChildPhase(update flowstore.ChildPhaseUpdate) (flowstore.FlowRecord, error)
	SetPlanLink(update flowstore.PlanLinkUpdate) (flowstore.FlowRecord, error)
	SetPR(update flowstore.PRUpdate) (flowstore.FlowRecord, error)
	SetMerge(update flowstore.MergeUpdate) (flowstore.FlowRecord, error)
	SetAutoMode(update flowstore.AutoModeUpdate) (flowstore.FlowRecord, error)
	SetStartMetadata(update flowstore.StartMetadataUpdate) (flowstore.FlowRecord, error)
	AddPhaseLaunchID(update flowstore.PhaseLaunchUpdate) (flowstore.FlowRecord, error)
	ResetAwaitingSessionPhase(update flowstore.PhaseResetUpdate) (flowstore.FlowRecord, error)
	AttachSession(update flowstore.SessionAttachUpdate) (flowstore.FlowRecord, error)
	Delete(flowID string) error
}
