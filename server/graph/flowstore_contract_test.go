package graph

import (
	"testing"

	"github.com/brian-bell/flowstate/flowrepo"
)

func TestFlowStoreMatchesRepositoryContract(t *testing.T) {
	var _ FlowStore = (flowrepo.FlowRepository)(nil)
	var _ flowrepo.FlowRepository = (FlowStore)(nil)
}
