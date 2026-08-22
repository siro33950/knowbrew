package invocation

import (
	invocationstate "github.com/siro33950/knowbrew/internal/adapters/invocation/state"
)

type Guard struct {
	Root string
}

func (guard Guard) ValidateFeedstock(feedstockID string) error {
	return invocationstate.ValidateFeedstock(feedstockID)
}

func (guard Guard) Mutate(change func() error) error {
	claim, err := invocationstate.Claim(guard.Root)
	if err != nil {
		return err
	}
	if err := change(); err != nil {
		invocationstate.Rollback(claim)
		return err
	}
	return nil
}
