// Package all registers every source connector.
package all

import (
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/cockroachdb"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/couchdb"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/dynamodb"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/kafka"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/mongodb"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/mysql"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/postgres"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/redisstream"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/sqlpoll"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/sqlserver"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/webhook"
)
