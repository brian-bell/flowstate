package flowrepo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brian-bell/flowstate/flowrepo"
	"github.com/brian-bell/flowstate/flowrepo/flowrepotest"
	"github.com/brian-bell/flowstate/flowstore"
	"github.com/brian-bell/flowstate/planstore"
)

func TestFakeContract(t *testing.T) {
	flowrepotest.RunContract(t, func(t testing.TB, clock *flowrepotest.ScriptedClock) flowrepotest.Fixture {
		t.Helper()
		fake := flowrepo.NewFake(flowrepo.FakeOptions{Now: clock.Now})
		return flowrepotest.Fixture{
			Repo: fake,
			SeedPlan: func(planID string) string {
				path := filepath.Join(t.TempDir(), "plans", planID, "plan.md")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte("# Plan\n"), 0o600); err != nil {
					t.Fatalf("WriteFile(%q) error = %v", path, err)
				}
				fake.SeedPlan(planID, path)
				return path
			},
			SeedPlanPhase: func(planID, phaseID string) string {
				path := filepath.Join(t.TempDir(), "plans", planID, "plan.md")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte("# Plan\n"), 0o600); err != nil {
					t.Fatalf("WriteFile(%q) error = %v", path, err)
				}
				fake.SeedPlan(planID, path)
				fake.SeedPlanPhase(planID, phaseID, "Plan", "pending", 1)
				return path
			},
			PlanPhaseStatus: func(planID, phaseID string) string {
				status, ok := fake.PlanPhaseStatus(planID, phaseID)
				if !ok {
					t.Fatalf("fake plan phase %s/%s not found", planID, phaseID)
				}
				return status
			},
			BreakPlanMetadata: func(planID string) {
				fake.BreakPlanMetadata(planID)
			},
			SeedUnreadablePlan: func(planID string) string {
				path := filepath.Join(t.TempDir(), "plans", planID, "plan.md")
				fake.SeedPlan(planID, path)
				return path
			},
		}
	})
}

func TestFlowStoreContract(t *testing.T) {
	flowrepotest.RunContract(t, func(t testing.TB, clock *flowrepotest.ScriptedClock) flowrepotest.Fixture {
		t.Helper()
		root := t.TempDir()
		store, err := flowstore.NewStore(flowstore.StoreOptions{
			Root: root,
			Now:  clock.Now,
		})
		if err != nil {
			t.Fatalf("NewStore() error = %v", err)
		}
		plans, err := planstore.NewStore(planstore.StoreOptions{Root: root, Now: clock.Now})
		if err != nil {
			t.Fatalf("planstore.NewStore() error = %v", err)
		}
		return flowrepotest.Fixture{
			Repo: store,
			SeedPlan: func(planID string) string {
				_, err := plans.Save(planstore.PlanRecord{
					PlanID:   planID,
					Title:    "Plan " + planID,
					Status:   "approved",
					Markdown: "# Plan\n",
				})
				if err != nil {
					t.Fatalf("Save plan %q error = %v", planID, err)
				}
				path, err := planstore.MarkdownPath(root, planID)
				if err != nil {
					t.Fatalf("MarkdownPath(%q) error = %v", planID, err)
				}
				return path
			},
			SeedPlanPhase: func(planID, phaseID string) string {
				_, err := plans.Save(planstore.PlanRecord{
					PlanID:   planID,
					Title:    "Plan " + planID,
					Status:   "approved",
					Markdown: "# Plan\n",
					Phases: []planstore.PlanPhase{{
						PhaseID: phaseID,
						Title:   "Plan",
						Status:  "pending",
						Order:   1,
					}},
				})
				if err != nil {
					t.Fatalf("Save plan phase %q error = %v", planID, err)
				}
				path, err := planstore.MarkdownPath(root, planID)
				if err != nil {
					t.Fatalf("MarkdownPath(%q) error = %v", planID, err)
				}
				return path
			},
			PlanPhaseStatus: func(planID, phaseID string) string {
				record, err := plans.ReadMetadata(planID)
				if err != nil {
					t.Fatalf("ReadMetadata(%q) error = %v", planID, err)
				}
				for _, phase := range record.Phases {
					if phase.PhaseID == phaseID {
						return phase.Status
					}
				}
				t.Fatalf("plan phase %s/%s not found", planID, phaseID)
				return ""
			},
			BreakPlanMetadata: func(planID string) {
				path := filepath.Join(root, "plans", planID, "meta.json")
				if err := os.Remove(path); err != nil {
					t.Fatalf("Remove(%q) error = %v", path, err)
				}
			},
			SeedUnreadablePlan: func(planID string) string {
				_, err := plans.Save(planstore.PlanRecord{
					PlanID:   planID,
					Title:    "Plan " + planID,
					Status:   "approved",
					Markdown: "# Plan\n",
				})
				if err != nil {
					t.Fatalf("Save unreadable plan %q error = %v", planID, err)
				}
				path, err := planstore.MarkdownPath(root, planID)
				if err != nil {
					t.Fatalf("MarkdownPath(%q) error = %v", planID, err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatalf("Remove(%q) error = %v", path, err)
				}
				return path
			},
		}
	})
}
