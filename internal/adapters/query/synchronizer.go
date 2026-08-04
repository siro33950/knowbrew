package query

import (
	"context"

	"github.com/siro33950/knowbrew/internal/adapters/embedding"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	searchapp "github.com/siro33950/knowbrew/internal/application/search"
)

type Synchronizer struct {
	Service searchapp.Service
	Encoder embedding.Encoder
}

func (synchronizer Synchronizer) Sync(ctx context.Context) ([]diagnostic.Warning, error) {
	_, warnings, err := synchronizer.Service.Synchronize(ctx, false)
	return warnings, err
}

func (synchronizer Synchronizer) Close() error {
	if synchronizer.Encoder == nil {
		return nil
	}
	return synchronizer.Encoder.Close()
}
