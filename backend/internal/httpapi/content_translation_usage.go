package httpapi

import (
	"context"
	"fmt"

	"github.com/yexca/kikoto/backend/internal/translation"
)

type translationBudgetUsage struct {
	cacheRows        int64
	contentCacheText int64
	contentTotalText int64
	persistentText   int64
	sourceText       int64
}

// Classify each row once in one snapshot. The budget, persistent share and
// page attribution all need the same expensive published-corpus predicate.
func (s *Server) translationBudgetUsage(ctx context.Context, cfg translation.Config) (translationBudgetUsage, error) {
	var usage translationBudgetUsage
	rows, err := s.db.QueryContext(ctx, `WITH classified AS MATERIALIZED (
		SELECT CASE WHEN ct.origin<>'model' THEN 'native'
			WHEN `+translationPersistentCorpusSQL("ct")+` THEN 'persistent' ELSE 'cache' END AS kind,
			length(CAST(ct.source_text AS BLOB))+length(CAST(ct.translated_text AS BLOB)) AS bytes
		FROM content_translation ct
	)
	SELECT kind,count(*),COALESCE(sum(bytes),0) FROM classified GROUP BY kind
	UNION ALL
	SELECT 'source',count(*),COALESCE(sum(length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB))),0)
	FROM source_field_translation`, translation.ConfigKey(cfg), translation.ConfigKey(cfg), translation.ConfigKey(cfg))
	if err != nil {
		return usage, fmt.Errorf("measure translation budget usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var count, bytes int64
		if err := rows.Scan(&kind, &count, &bytes); err != nil {
			return usage, err
		}
		switch kind {
		case "source":
			usage.cacheRows += count
			usage.sourceText = bytes
		case "cache":
			usage.cacheRows += count
			usage.contentCacheText = bytes
			usage.contentTotalText += bytes
		case "persistent":
			usage.persistentText = bytes
			usage.contentTotalText += bytes
		case "native":
			usage.contentTotalText += bytes
		}
	}
	return usage, rows.Err()
}
