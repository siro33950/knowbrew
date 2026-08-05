package source

import (
	"fmt"
	"slices"
	"strings"

	"github.com/siro33950/knowbrew/internal/domain"
)

type CandidateSet struct {
	Source     string
	Candidates []domain.FeedstockCandidate
}

func MergeCandidateSets(sets []CandidateSet) ([]domain.FeedstockCandidate, error) {
	merged := make([]domain.FeedstockCandidate, 0)
	indexes := make(map[string]int)
	sources := make(map[string]string)
	for _, set := range sets {
		for _, candidate := range set.Candidates {
			index, exists := indexes[candidate.ID]
			if exists {
				if !sameCandidate(merged[index], candidate) {
					return nil, fmt.Errorf(
						"conflicting source turn %s in %s and %s",
						candidate.TurnID, sources[candidate.ID], set.Source,
					)
				}
				continue
			}
			indexes[candidate.ID] = len(merged)
			sources[candidate.ID] = set.Source
			merged = append(merged, candidate)
		}
	}
	slices.SortStableFunc(merged, func(left, right domain.FeedstockCandidate) int {
		if compared := left.Timestamp.Compare(right.Timestamp); compared != 0 {
			return compared
		}
		return 0
	})
	for index := range merged {
		merged[index].SourceSequence = int64(index + 1)
	}
	return merged, nil
}

func sameCandidate(left, right domain.FeedstockCandidate) bool {
	return left.ID == right.ID &&
		left.TurnID == right.TurnID &&
		left.Session == right.Session &&
		left.Timestamp.Equal(right.Timestamp) &&
		left.Agent == right.Agent &&
		left.CWD == right.CWD &&
		left.Repo == right.Repo &&
		left.Branch == right.Branch &&
		slices.Equal(left.Dialogue, right.Dialogue)
}

func SessionKey(candidate domain.FeedstockCandidate) string {
	return strings.TrimSpace(candidate.Agent) + "\x00" + strings.TrimSpace(candidate.Session.ID)
}
