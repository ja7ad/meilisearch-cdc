package diff

import (
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/memory"
)

func sourceNew(table string) (*memory.Source, error) {
	s, err := source.New(model.Source{Type: memory.Type, Table: table}, "t")
	if err != nil {
		return nil, err
	}
	return s.(*memory.Source), nil
}
