package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/persistence/knowledgefmt"
	"github.com/siro33950/knowbrew/internal/adapters/persistence/markdownstore"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/application/storage"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Markdown struct {
	Store *store.Store
}

func New(root string) (*Markdown, error) {
	dataStore, err := store.New(root)
	if err != nil {
		return nil, err
	}
	return &Markdown{Store: dataStore}, nil
}

func (repository *Markdown) EnsureLayout() error {
	return repository.Store.EnsureLayout()
}

func (repository *Markdown) WithLock(ctx context.Context, change func() error) error {
	return repository.Store.WithLock(ctx, change)
}

func (repository *Markdown) ListFeedstocks() (
	[]domain.Feedstock,
	[]diagnostic.Warning,
	error,
) {
	return repository.Store.ListFeedstocks()
}

func (repository *Markdown) GetFeedstock(id string) (domain.Feedstock, error) {
	feedstock, _, err := repository.Store.FindFeedstock(id)
	return feedstock, err
}

func (repository *Markdown) WriteFeedstock(feedstock domain.Feedstock) error {
	return repository.Store.WriteFeedstock(feedstock)
}

func (repository *Markdown) SummarizeFeedstock(id, summary string) error {
	return repository.Store.SummarizeFeedstock(id, summary)
}

func (repository *Markdown) AnnotateFeedstock(
	id string,
	types []domain.KnowledgeType,
	when time.Time,
) error {
	return repository.Store.AnnotateFeedstock(id, types, when)
}

func (repository *Markdown) EnsureMaster(kind string, entry domain.MasterEntry) (bool, error) {
	return repository.Store.EnsureMaster(kind, entry)
}

func (repository *Markdown) WriteBrewedFeedstock(feedstock domain.Feedstock, when time.Time) error {
	return repository.Store.WriteBrewedFeedstock(feedstock, when)
}

func (repository *Markdown) LoadMasters(kind string) (
	[]domain.MasterEntry,
	[]diagnostic.Warning,
	error,
) {
	return repository.Store.LoadMasters(kind)
}

func (repository *Markdown) KnowledgeTypes() ([]domain.MasterEntry, error) {
	return repository.Store.KnowledgeTypes()
}

func (repository *Markdown) ReadWritingGuide(name string) (string, bool, error) {
	return repository.Store.ReadWritingGuide(name)
}

func (repository *Markdown) LoadTemplates() (
	[]domain.DocumentTemplate,
	[]diagnostic.Warning,
	error,
) {
	return repository.Store.LoadTemplates()
}

func (repository *Markdown) ReadDistilledDocument(
	template domain.DocumentTemplate,
	subject string,
) (domain.DistilledDocument, bool, error) {
	return repository.Store.ReadDistilledDocument(template, subject)
}

func (repository *Markdown) WriteDistilledDocument(
	template domain.DocumentTemplate,
	document domain.DistilledDocument,
) error {
	return repository.Store.WriteDistilledDocument(template, document)
}

func (repository *Markdown) DeleteDistilledDocument(
	template domain.DocumentTemplate,
	subject string,
) (bool, error) {
	return repository.Store.DeleteDistilledDocument(template, subject)
}

func (repository *Markdown) ListKnowledge() (
	[]storage.KnowledgeDocument,
	[]diagnostic.Warning,
	error,
) {
	files, warnings, err := repository.Store.ListAllKnowledge()
	if err != nil {
		return nil, warnings, err
	}
	documents := make([]storage.KnowledgeDocument, 0, len(files))
	for _, file := range files {
		document, decodeErr := repository.document(file)
		if decodeErr != nil {
			return nil, warnings, decodeErr
		}
		documents = append(documents, document)
	}
	return documents, warnings, nil
}

func (repository *Markdown) ListKnowledgeMetadata() (
	[]storage.KnowledgeMetadata,
	[]diagnostic.Warning,
	error,
) {
	files, warnings, err := repository.Store.ListAllKnowledge()
	if err != nil {
		return nil, warnings, err
	}
	metadata := make([]storage.KnowledgeMetadata, 0, len(files))
	for _, file := range files {
		metadata = append(metadata, storage.KnowledgeMetadata{
			Knowledge: file.Knowledge, Location: file.Path,
		})
	}
	return metadata, warnings, nil
}

func (repository *Markdown) FindKnowledge(id string) (storage.KnowledgeDocument, error) {
	file, err := repository.Store.FindKnowledge(id)
	if err != nil {
		return storage.KnowledgeDocument{}, err
	}
	return repository.document(file)
}

func (repository *Markdown) document(file store.KnowledgeFile) (storage.KnowledgeDocument, error) {
	statement, rationale, err := knowledgefmt.Decode(file.Body)
	if err != nil {
		return storage.KnowledgeDocument{}, fmt.Errorf("read knowledge %s: %w", file.Knowledge.ID, err)
	}
	digest, err := store.FileDigest(file.Path)
	if err != nil {
		return storage.KnowledgeDocument{}, err
	}
	return storage.KnowledgeDocument{
		Knowledge: file.Knowledge, Statement: statement, Rationale: rationale, Digest: digest,
		Location: file.Path,
	}, nil
}

func (repository *Markdown) Transaction(
	ctx context.Context,
	change func(storage.Transaction) error,
) error {
	return repository.Store.Transaction(ctx, func(transaction *store.Transaction) error {
		return change(markdownTransaction{transaction: transaction, store: repository.Store})
	})
}

type markdownTransaction struct {
	transaction *store.Transaction
	store       *store.Store
}

func (transaction markdownTransaction) StageKnowledge(record domain.KnowledgeRecord) error {
	body, err := knowledgefmt.Encode(record.Statement, record.Rationale)
	if err != nil {
		return err
	}
	return transaction.transaction.StageKnowledge(record.Knowledge, body)
}

func (transaction markdownTransaction) StageKnowledgeMetadata(knowledge domain.Knowledge) error {
	file, err := transaction.store.FindKnowledge(knowledge.ID)
	if err != nil {
		return err
	}
	return transaction.transaction.StageKnowledge(knowledge, file.Body)
}

func (transaction markdownTransaction) StageBrewedFeedstock(
	feedstock domain.Feedstock,
	when time.Time,
) error {
	return transaction.transaction.StageBrewedFeedstock(feedstock, when)
}
