package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
	"github.com/siro33950/knowbrew/internal/domain"
)

const missingFileDigest = "missing"

type transactionMutation struct {
	Path         string `json:"path"`
	BeforeDigest string `json:"before_digest"`
	AfterDigest  string `json:"after_digest"`
	Mode         uint32 `json:"mode"`
	Data         []byte `json:"data"`
}

type transactionJournal struct {
	Created   time.Time             `json:"created"`
	Mutations []transactionMutation `json:"mutations"`
}

type Transaction struct {
	store     *Store
	mutations map[string]transactionMutation
}

func (s *Store) Transaction(ctx context.Context, fn func(*Transaction) error) error {
	return s.WithLock(ctx, func() error {
		tx := &Transaction{store: s, mutations: make(map[string]transactionMutation)}
		if err := fn(tx); err != nil {
			return err
		}
		return tx.commit()
	})
}

func (tx *Transaction) StageKnowledge(knowledge domain.Knowledge, body string) error {
	knowledge.Subject = domain.MasterName(knowledge.Subject)
	knowledge.EstablishedBy = domain.MasterName(knowledge.EstablishedBy)
	knowledge.Feedstocks = normalizeFeedstockLinks(knowledge.Feedstocks)
	knowledge.Supersedes = normalizeKnowledgeLinks(knowledge.Supersedes)
	knowledge.SupersededBy = domain.MasterName(knowledge.SupersededBy)
	knowledge.Status = domain.EffectiveKnowledgeStatus(knowledge)
	types, err := tx.store.NormalizeKnowledgeTypes([]domain.KnowledgeType{knowledge.Type})
	if err != nil {
		return fmt.Errorf("knowledge type: %w", err)
	}
	knowledge.Type = types[0]
	if err := domain.ValidateKnowledge(knowledge); err != nil {
		return err
	}
	if body == "" {
		return errors.New("knowledge body is required")
	}
	data, err := encodeKnowledge(knowledge, body)
	if err != nil {
		return err
	}
	path, err := tx.store.KnowledgePath(knowledge.ID)
	if err != nil {
		return err
	}
	return tx.stage(path, data, 0o644)
}

func (tx *Transaction) StageBrewedFeedstock(feedstock domain.Feedstock, when time.Time) error {
	current, path, err := tx.store.FindFeedstock(feedstock.ID)
	if err != nil {
		return err
	}
	if err := current.ApplyBrewProgress(when); err != nil {
		return err
	}
	data, err := encodeWithWikilinks(current, "", "types")
	if err != nil {
		return err
	}
	return tx.stage(path, data, 0o644)
}

func (tx *Transaction) stage(path string, data []byte, mode os.FileMode) error {
	relative, err := filepath.Rel(tx.store.Root, path)
	if err != nil || relative == ".." || filepath.IsAbs(relative) {
		return errors.New("transaction path escapes the configured root")
	}
	before, err := fileDigest(path)
	if err != nil {
		return err
	}
	tx.mutations[relative] = transactionMutation{
		Path: relative, BeforeDigest: before, AfterDigest: digestBytes(data),
		Mode: uint32(mode.Perm()), Data: append([]byte(nil), data...),
	}
	return nil
}

func (tx *Transaction) commit() error {
	if len(tx.mutations) == 0 {
		return nil
	}
	mutations := make([]transactionMutation, 0, len(tx.mutations))
	for _, mutation := range tx.mutations {
		mutations = append(mutations, mutation)
	}
	sort.Slice(mutations, func(i, j int) bool { return mutations[i].Path < mutations[j].Path })
	journal := transactionJournal{Created: time.Now().UTC(), Mutations: mutations}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	directory, err := fsutil.ResolveWithin(tx.store.Root, ".knowbrew", "state", "transactions")
	if err != nil {
		return err
	}
	name := fmt.Sprintf("tx-%d-%s.json", time.Now().UnixNano(), digestBytes(data)[:12])
	path := filepath.Join(directory, name)
	if err := fsutil.AtomicWrite(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write transaction journal: %w", err)
	}
	if err := applyJournal(tx.store.Root, journal); err != nil {
		return fmt.Errorf("apply transaction %s: %w", name, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove transaction journal: %w", err)
	}
	return nil
}

func (s *Store) recoverTransactionsLocked() error {
	directory, err := fsutil.ResolveWithin(s.Root, ".knowbrew", "state", "transactions")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var journal transactionJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return fmt.Errorf("decode transaction journal %s: %w", path, err)
		}
		if err := applyJournal(s.Root, journal); err != nil {
			return fmt.Errorf("recover transaction journal %s: %w", path, err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func applyJournal(root string, journal transactionJournal) error {
	for _, mutation := range journal.Mutations {
		path, err := fsutil.ResolveWithin(root, mutation.Path)
		if err != nil {
			return err
		}
		current, err := fileDigest(path)
		if err != nil {
			return err
		}
		if current == mutation.AfterDigest {
			continue
		}
		if current != mutation.BeforeDigest {
			return fmt.Errorf("%s changed outside the transaction", mutation.Path)
		}
		if digestBytes(mutation.Data) != mutation.AfterDigest {
			return fmt.Errorf("transaction data digest mismatch for %s", mutation.Path)
		}
		if err := fsutil.AtomicWrite(path, mutation.Data, os.FileMode(mutation.Mode)); err != nil {
			return err
		}
	}
	return nil
}

func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return missingFileDigest, nil
	}
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func FileDigest(path string) (string, error) {
	return fileDigest(path)
}
