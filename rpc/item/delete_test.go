package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/item"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/db"
	"eigenflux_server/rpc/item/dal"
	profiledal "eigenflux_server/rpc/profile/dal"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDeleteMyItemLogic(t *testing.T) {
	t.Run("AuthorizationCheck", func(t *testing.T) {
		gdb := newDeleteItemTestDB(t)
		seedDeleteItem(t, gdb, 101, 201)

		resp, err := (&ItemServiceImpl{}).DeleteMyItem(context.Background(), &item.DeleteMyItemReq{
			ItemId: 201, AuthorAgentId: 102,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.BaseResp)
		require.Equal(t, int32(403), resp.BaseResp.Code)
		assertDeleteItemStatus(t, gdb, 201, dal.StatusCompleted)
	})

	t.Run("StatusUpdate", func(t *testing.T) {
		gdb := newDeleteItemTestDB(t)
		seedDeleteItem(t, gdb, 101, 201)
		seedDeleteItem(t, gdb, 102, 202)

		resp, err := (&ItemServiceImpl{}).DeleteMyItem(context.Background(), &item.DeleteMyItemReq{
			ItemId: 201, AuthorAgentId: 101,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.BaseResp)
		require.Zero(t, resp.BaseResp.Code)
		assertDeleteItemStatus(t, gdb, 201, dal.StatusDeleted)
		assertDeleteItemStatus(t, gdb, 202, dal.StatusCompleted)
	})

	t.Run("ErrorHandling", func(t *testing.T) {
		t.Run("NotFound", func(t *testing.T) {
			newDeleteItemTestDB(t)
			resp, err := (&ItemServiceImpl{}).DeleteMyItem(context.Background(), &item.DeleteMyItemReq{
				ItemId: 201, AuthorAgentId: 101,
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NotNil(t, resp.BaseResp)
			require.Equal(t, int32(404), resp.BaseResp.Code)
		})

		t.Run("LookupFailure", func(t *testing.T) {
			gdb := newDeleteItemTestDB(t)
			seedDeleteItem(t, gdb, 101, 201)
			require.NoError(t, gdb.Callback().Query().Before("gorm:query").Register("test:stats_lookup_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "item_stats" {
					tx.AddError(errors.New("injected stats lookup failure"))
				}
			}))

			resp, err := (&ItemServiceImpl{}).DeleteMyItem(context.Background(), &item.DeleteMyItemReq{
				ItemId: 201, AuthorAgentId: 101,
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NotNil(t, resp.BaseResp)
			require.Equal(t, int32(500), resp.BaseResp.Code)
			assertDeleteItemStatus(t, gdb, 201, dal.StatusCompleted)
		})

		t.Run("UpdateFailure", func(t *testing.T) {
			gdb := newDeleteItemTestDB(t)
			seedDeleteItem(t, gdb, 101, 201)
			require.NoError(t, gdb.Exec(`CREATE TRIGGER reject_item_status_update
				BEFORE UPDATE OF status ON processed_items
				BEGIN SELECT RAISE(ABORT, 'injected status update failure'); END`).Error)

			resp, err := (&ItemServiceImpl{}).DeleteMyItem(context.Background(), &item.DeleteMyItemReq{
				ItemId: 201, AuthorAgentId: 101,
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NotNil(t, resp.BaseResp)
			require.Equal(t, int32(500), resp.BaseResp.Code)
			assertDeleteItemStatus(t, gdb, 201, dal.StatusCompleted)
		})
	})
}

// TestDeleteMyItemIntegration exercises the real handler and DAL against an
// isolated SQLite database with the item_stats foreign keys enforced.
func TestDeleteMyItemIntegration(t *testing.T) {
	gdb := newDeleteItemTestDB(t)
	const authorID, itemID = int64(101), int64(201)
	seedDeleteItem(t, gdb, authorID, itemID)

	// The database must reject the missing-author fixture that would otherwise
	// hide a foreign-key failure on PostgreSQL.
	require.NoError(t, dal.CreateRawItem(gdb, &dal.RawItem{
		ItemID: 202, AuthorAgentID: 102, RawContent: "Missing author",
	}))
	require.Error(t, dal.CreateItemStats(gdb, 202, 102))

	svc := &ItemServiceImpl{}
	resp, err := svc.DeleteMyItem(context.Background(), &item.DeleteMyItemReq{
		ItemId: itemID, AuthorAgentId: authorID,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.BaseResp)
	require.Zero(t, resp.BaseResp.Code)
	assertDeleteItemStatus(t, gdb, itemID, dal.StatusDeleted)

	// A soft delete preserves the author, original content, and attribution.
	author, err := profiledal.GetAgentByID(gdb, authorID)
	require.NoError(t, err)
	require.Equal(t, authorID, author.AgentID)
	raw, err := dal.GetRawItemByID(gdb, itemID)
	require.NoError(t, err)
	require.Equal(t, "Test content", raw.RawContent)
	stats, err := dal.GetItemStatsByID(gdb, itemID)
	require.NoError(t, err)
	require.Equal(t, authorID, stats.AuthorAgentID)

	resp, err = svc.DeleteMyItem(context.Background(), &item.DeleteMyItemReq{
		ItemId: itemID, AuthorAgentId: 102,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.BaseResp)
	require.Equal(t, int32(403), resp.BaseResp.Code)
	assertDeleteItemStatus(t, gdb, itemID, dal.StatusDeleted)
}

func newDeleteItemTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "delete-item.sqlite")+"?_foreign_keys=on"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	previous := db.DB
	db.DB = gdb
	t.Cleanup(func() {
		db.DB = previous
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, gdb.AutoMigrate(&profiledal.Agent{}, &dal.RawItem{}, &dal.ProcessedItem{}))
	require.NoError(t, gdb.Exec("CREATE UNIQUE INDEX idx_delete_agent_short_id ON agents(short_id) WHERE short_id IS NOT NULL").Error)
	// DAL structs have no association tags; retain the migration's two foreign
	// keys explicitly so the SQLite fixture enforces author/item existence.
	require.NoError(t, gdb.Exec(`CREATE TABLE item_stats (
		item_id BIGINT PRIMARY KEY REFERENCES raw_items(item_id) ON DELETE CASCADE,
		author_agent_id BIGINT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
		consumed_count BIGINT NOT NULL DEFAULT 0,
		score_neg1_count BIGINT NOT NULL DEFAULT 0,
		score_0_count BIGINT NOT NULL DEFAULT 0,
		score_1_count BIGINT NOT NULL DEFAULT 0,
		score_2_count BIGINT NOT NULL DEFAULT 0,
		total_score BIGINT NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`).Error)
	return gdb
}

func seedDeleteItem(t *testing.T, gdb *gorm.DB, authorID, itemID int64) {
	t.Helper()
	shortID, err := agentidentity.GenerateShortID()
	require.NoError(t, err)
	require.NoError(t, gdb.Create(&profiledal.Agent{
		AgentID: authorID, ShortID: shortID, Email: fmt.Sprintf("delete-%d@test.local", authorID),
		AgentName: "Delete test author", CreatedAt: 1, UpdatedAt: 1,
	}).Error)
	require.NoError(t, dal.CreateRawItem(gdb, &dal.RawItem{
		ItemID: itemID, AuthorAgentID: authorID, RawContent: "Test content",
	}))
	require.NoError(t, dal.CreateProcessedItem(gdb, &dal.ProcessedItem{
		ItemID: itemID, Status: dal.StatusCompleted,
	}))
	require.NoError(t, dal.CreateItemStats(gdb, itemID, authorID))
}

func assertDeleteItemStatus(t *testing.T, gdb *gorm.DB, itemID int64, want int16) {
	t.Helper()
	var got dal.ProcessedItem
	require.NoError(t, gdb.First(&got, "item_id = ?", itemID).Error)
	require.Equal(t, want, got.Status)
}
