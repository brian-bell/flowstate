package flowrepo_test

import (
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
		}
	})
}
