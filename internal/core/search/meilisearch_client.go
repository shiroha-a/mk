package search

import (
	"encoding/json"
	"fmt"

	meili "github.com/meilisearch/meilisearch-go"
)

// MeilisearchClient adapts the upstream meilisearch-go SDK to the
// MeilisearchIndex interface this package consumes.
//
// Construction is split into NewMeilisearchService (one connection per
// process) and OpenIndex (one wrapper per index name) so the same SDK client
// can be reused for multiple Misskey indices in the future (e.g. notes,
// users, hashtags).
type MeilisearchClient struct {
	index meili.IndexManager
}

// NewMeilisearchService dials the Meilisearch host and returns the underlying
// SDK service manager.  Callers must hold on to the returned ServiceManager
// (e.g. on `Server`) so that it survives for the process lifetime.
func NewMeilisearchService(host, apiKey string) meili.ServiceManager {
	if apiKey == "" {
		return meili.New(host)
	}
	return meili.New(host, meili.WithAPIKey(apiKey))
}

// OpenIndex returns a MeilisearchIndex wrapper for the given index name on
// the supplied service manager.
func OpenIndex(svc meili.ServiceManager, indexName string) *MeilisearchClient {
	return &MeilisearchClient{index: svc.Index(indexName)}
}

// AddDocuments uploads (and updates) documents in the index.
func (c *MeilisearchClient) AddDocuments(docs []NoteDocument) error {
	if c == nil || c.index == nil {
		return nil
	}
	if len(docs) == 0 {
		return nil
	}
	primaryKey := "id"
	_, err := c.index.AddDocuments(docs, &meili.DocumentOptions{PrimaryKey: &primaryKey})
	return err
}

// DeleteDocument removes a single document by id.
func (c *MeilisearchClient) DeleteDocument(id string) error {
	if c == nil || c.index == nil {
		return nil
	}
	_, err := c.index.DeleteDocument(id, nil)
	return err
}

// Search runs a search query and returns the trimmed-down hit list this
// package needs (id + createdAt only).
func (c *MeilisearchClient) Search(query string, req IndexSearchRequest) (*IndexSearchResponse, error) {
	if c == nil || c.index == nil {
		return &IndexSearchResponse{}, nil
	}
	sdkReq := &meili.SearchRequest{
		Limit:                req.Limit,
		Offset:               req.Offset,
		AttributesToRetrieve: req.AttributesToRetrieve,
		Sort:                 req.Sort,
	}
	if req.Filter != "" {
		sdkReq.Filter = req.Filter
	}
	if req.MatchingStrategy != "" {
		sdkReq.MatchingStrategy = meili.MatchingStrategy(req.MatchingStrategy)
	}
	resp, err := c.index.Search(query, sdkReq)
	if err != nil {
		return nil, err
	}
	out := &IndexSearchResponse{Hits: make([]IndexSearchHit, 0, len(resp.Hits))}
	for _, hit := range resp.Hits {
		converted, err := decodeHit(hit)
		if err != nil {
			return nil, err
		}
		out.Hits = append(out.Hits, converted)
	}
	return out, nil
}

// UpdateSettings translates our neutral IndexSettings into the SDK shape and
// pushes them to Meilisearch.
func (c *MeilisearchClient) UpdateSettings(settings IndexSettings) error {
	if c == nil || c.index == nil {
		return nil
	}
	sdkSettings := &meili.Settings{
		SearchableAttributes: settings.SearchableAttributes,
		SortableAttributes:   settings.SortableAttributes,
		FilterableAttributes: settings.FilterableAttributes,
		TypoTolerance:        &meili.TypoTolerance{Enabled: settings.TypoToleranceEnabled},
	}
	if settings.PaginationMaxHits > 0 {
		sdkSettings.Pagination = &meili.Pagination{MaxTotalHits: settings.PaginationMaxHits}
	}
	_, err := c.index.UpdateSettings(sdkSettings)
	return err
}

// decodeHit pulls out the id and (optional) createdAt fields from a generic
// SDK Hit.  Anything else in the document is ignored.
func decodeHit(h meili.Hit) (IndexSearchHit, error) {
	out := IndexSearchHit{}
	if raw, ok := h["id"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err != nil {
			return out, fmt.Errorf("failed to decode hit id: %w", err)
		}
		out.ID = id
	}
	if raw, ok := h["createdAt"]; ok {
		var createdAt int64
		if err := json.Unmarshal(raw, &createdAt); err == nil {
			out.CreatedAt = createdAt
		}
	}
	return out, nil
}
