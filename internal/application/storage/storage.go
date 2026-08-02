package storage

import (
	"time"

	"github.com/siro33950/knowbrew/internal/domain"
)

type KnowledgeDocument struct {
	Knowledge domain.Knowledge
	Statement string
	Rationale string
	Digest    string
	Location  string
}

type KnowledgeMetadata struct {
	Knowledge domain.Knowledge
	Location  string
}

type Transaction interface {
	StageKnowledge(domain.KnowledgeRecord) error
	StageKnowledgeMetadata(domain.Knowledge) error
	StageBrewedFeedstock(domain.Feedstock, time.Time) error
}
