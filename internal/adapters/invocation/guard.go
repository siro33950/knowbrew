package invocation

import (
	"os"
	"strings"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	invocationstate "github.com/siro33950/knowbrew/internal/adapters/invocation/state"
	"github.com/siro33950/knowbrew/internal/application/agent"
)

type Guard struct {
	Root string
}

func (guard Guard) ValidateFeedstock(feedstockID string) error {
	return invocationstate.ValidateFeedstock(feedstockID)
}

func (guard Guard) ValidateAssertion(assertionID string) error {
	return invocationstate.ValidateAssertion(assertionID)
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

func (guard Guard) IsAssertionInvocation() bool {
	return strings.TrimSpace(os.Getenv(config.InvocationIDEnvironment)) != "" &&
		strings.TrimSpace(os.Getenv(config.InvocationFeedstockEnvironment)) != "" &&
		strings.TrimSpace(os.Getenv(config.InvocationAssertionEnvironment)) != ""
}

func (guard Guard) RecordCatalog(subject string, ids []string, digest string) error {
	return invocationstate.RecordCatalog(guard.Root, subject, ids, digest)
}

func (guard Guard) RecordInspected(ids []string) error {
	return invocationstate.RecordInspected(guard.Root, ids)
}

func (guard Guard) ReadState() (agent.ReadState, error) {
	state, err := invocationstate.CurrentReadState(guard.Root)
	if err != nil {
		return agent.ReadState{}, err
	}
	return agent.ReadState{
		Subject: state.Subject, Catalog: append([]string(nil), state.Catalog...),
		CatalogDigest: state.CatalogDigest, Inspected: append([]string(nil), state.Inspected...),
		AnnotationContext: state.AnnotationContext,
	}, nil
}
