package flowrepo_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/flowrepo"
	"github.com/brian-bell/flowstate/flowstore"
)

// compile assertion: the filesystem store satisfies the shared repository contract.
var _ flowrepo.FlowRepository = (*flowstore.Store)(nil)

func TestFlowStoreSatisfiesRepositoryRoundTrip(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	store, err := flowstore.NewStore(flowstore.StoreOptions{
		Root: root,
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	var repo flowrepo.FlowRepository = store
	created, err := repo.Create(flowstore.FlowRecord{
		Title:        "Repository round trip",
		Instructions: "Prove the interface is usable.",
		RepoPath:     filepath.Join(root, "repo"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	read, err := repo.Read(created.FlowID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if read.FlowID != created.FlowID {
		t.Fatalf("Read FlowID = %q, want %q", read.FlowID, created.FlowID)
	}
}
