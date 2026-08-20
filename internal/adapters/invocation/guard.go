package invocation

import (
	"os"
	"strings"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	invocationstate "github.com/siro33950/knowbrew/internal/adapters/invocation/state"
	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/domain"
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

func (guard Guard) IsBrewInvocation() bool {
	return strings.TrimSpace(os.Getenv(config.InvocationIDEnvironment)) != "" &&
		strings.TrimSpace(os.Getenv(config.InvocationFeedstockEnvironment)) != ""
}

func (guard Guard) RecordCatalog(subject string, ids []string, digest string) error {
	return invocationstate.RecordCatalog(guard.Root, subject, ids, digest)
}

func (guard Guard) RecordInspected(ids []string) error {
	return invocationstate.RecordInspected(guard.Root, ids)
}

func (guard Guard) RecordSubmitted(candidate domain.KnowledgeCandidate) error {
	return invocationstate.RecordSubmitted(guard.Root, candidate)
}

func (guard Guard) ReadState() (agent.ReadState, error) {
	state, err := invocationstate.CurrentReadState(guard.Root)
	if err != nil {
		return agent.ReadState{}, err
	}
	subjects := make(map[string]agent.SubjectReadState, len(state.Subjects))
	for subject, entry := range state.Subjects {
		subjects[subject] = agent.SubjectReadState{
			Catalog: append([]string(nil), entry.Catalog...),
			Digest:  entry.Digest,
		}
	}
	return agent.ReadState{
		Subjects: subjects, Inspected: append([]string(nil), state.Inspected...),
		Submitted:         append([]domain.KnowledgeCandidate(nil), state.Submitted...),
		AnnotationContext: state.AnnotationContext,
	}, nil
}
