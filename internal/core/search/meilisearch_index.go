package search

// MeilisearchIndex is the minimal subset of operations the Meilisearch
// provider needs.  It is intentionally narrow so tests can supply an
// in-memory fake without depending on the upstream meilisearch-go SDK types.
//
// Production code wires this interface up via meilisearch_client.go which
// translates between this neutral shape and the meilisearch-go SDK.
type MeilisearchIndex interface {
	AddDocuments(docs []NoteDocument) error
	DeleteDocument(id string) error
	Search(query string, req IndexSearchRequest) (*IndexSearchResponse, error)
	UpdateSettings(settings IndexSettings) error
}

// IndexSearchRequest is the neutral shape for a Meilisearch index search.
type IndexSearchRequest struct {
	// Filter is a Meilisearch filter expression (e.g. "userId = 'abc'").
	Filter string
	// Sort is a list of `field:dir` clauses (e.g. ["createdAt:desc"]).
	Sort []string
	// Limit caps the number of returned hits.
	Limit int64
	// Offset is the OFFSET-based pagination offset (upstream notes/search.ts:47)。
	Offset int64
	// AttributesToRetrieve narrows down the fields returned per hit.
	AttributesToRetrieve []string
	// MatchingStrategy mirrors the Meilisearch option of the same name.
	MatchingStrategy string
}

// IndexSearchResponse is the subset of the Meilisearch search response we
// actually consume.
type IndexSearchResponse struct {
	Hits []IndexSearchHit
}

// IndexSearchHit is a single document returned by the index. CreatedAt is
// kept around so the provider can sort by descending creation time using only
// the values returned in the search response (no extra DB roundtrip).
type IndexSearchHit struct {
	ID        string
	CreatedAt int64
}

// IndexSettings groups the index-side settings the provider applies on
// startup. Mirrors the relevant subset of the meilisearch-go Settings struct.
type IndexSettings struct {
	SearchableAttributes []string
	SortableAttributes   []string
	FilterableAttributes []string
	TypoToleranceEnabled bool
	PaginationMaxHits    int64
}
