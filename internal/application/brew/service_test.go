package brew

import (
	persistenceadapter "github.com/siro33950/knowbrew/internal/adapters/persistence"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
)

func repositoryForTest(dataStore *store.Store) *persistenceadapter.Markdown {
	return &persistenceadapter.Markdown{Store: dataStore}
}
