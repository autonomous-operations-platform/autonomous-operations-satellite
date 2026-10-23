package doris

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"time"
)

const defaultMaxOpenConns = 5

type Params struct {
	Addr, User, Password, DB string
	// MaxOpenConns caps the connection pool. Defaults to defaultMaxOpenConns when zero.
	MaxOpenConns int

	// Write batching. Zero values use the defaults applied in NewStore.
	// WriteBatchSize is the maximum number of records per multi-row INSERT.
	WriteBatchSize int
	// WriteBatchInterval is the maximum time the batcher waits before flushing
	// a partial batch. Lower values reduce write latency at the cost of smaller
	// batches.
	WriteBatchInterval time.Duration
	// WriteBatchTimeout is the per-flush deadline passed to ExecContext. It
	// must cover the execution time of a full-size multi-row INSERT under load.
	WriteBatchTimeout time.Duration
	// WriteChanSize is the per-table write batcher channel buffer depth.
	WriteChanSize int
}

func Connect(ctx context.Context, p Params, logger *slog.Logger) (*DorisStore, error) {
	if logger == nil {
		logger = slog.Default()
	}

	var db *sql.DB
	var err error
	for i := range 30 {
		db, err = openDB(p)
		if err == nil {
			break
		}
		logger.Warn("waiting for Doris", "component", "store", "attempt", i+1, "max", 30, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("connect to Doris: %w", err)
	}

	var store *DorisStore
	for i := range 30 {
		store, err = NewStore(db, p, logger)
		if err == nil {
			break
		}
		logger.Warn("waiting for Doris backend", "component", "store", "attempt", i+1, "max", 30, "err", err)
		select {
		case <-ctx.Done():
			_ = db.Close()
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init store: %w", err)
	}

	return store, nil
}

func openDB(p Params) (*sql.DB, error) {
	adminDB, err := sql.Open("mysql", dsn(p, ""))
	if err != nil {
		return nil, err
	}
	if err := adminDB.Ping(); err != nil {
		_ = adminDB.Close()
		return nil, err
	}
	if _, err := adminDB.Exec("CREATE DATABASE IF NOT EXISTS `" + p.DB + "`"); err != nil {
		_ = adminDB.Close()
		return nil, fmt.Errorf("create database: %w", err)
	}
	_ = adminDB.Close()

	db, err := sql.Open("mysql", dsn(p, p.DB))
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	maxConns := p.MaxOpenConns
	if maxConns == 0 {
		maxConns = defaultMaxOpenConns
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	return db, nil
}

func dsn(p Params, dbName string) string {
	userinfo := url.UserPassword(p.User, p.Password).String()
	if dbName == "" {
		return fmt.Sprintf("%s@%s/?collation=utf8mb4_general_ci", userinfo, p.Addr)
	}
	return fmt.Sprintf("%s@%s/%s?collation=utf8mb4_general_ci", userinfo, p.Addr, url.PathEscape(dbName))
}
