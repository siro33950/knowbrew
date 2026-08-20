package brew

import (
	invocationadapter "github.com/siro33950/knowbrew/internal/adapters/invocation"
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
)

func repositoryForTest(dataStore *store.Store) *persistenceadapter.Markdown {
	return &persistenceadapter.Markdown{Store: dataStore}
}

func invocationForTest(dataStore *store.Store) invocationadapter.Guard {
	return invocationadapter.Guard{Root: dataStore.Root}
}
