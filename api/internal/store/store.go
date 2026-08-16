// Package store 封装数据库连接池与事务，向上层暴露 sqlc 生成的 Querier。
//
// 上层 handler 依赖 dbgen.Querier 接口而不是具体结构体（sqlc 的 emit_interface），
// 这样单测可以塞假实现，不必起真库。
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

// Store 持有连接池，并组合出 Querier。
type Store struct {
	Pool *pgxpool.Pool
	*dbgen.Queries
}

// Open 建立连接池。
//
// maxConns 由 config 传入而不是在这里写死，但它的取值是**倒推**出来的：
// ADR 0005 选的 db-f1-micro 连接数很少，Cloud Run 又会横向扩容，
// 所以 max-instances × maxConns 必须小于实例连接上限。改一个就得改另一个。
func Open(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析数据库连接串失败: %w", err)
	}

	cfg.MaxConns = maxConns
	// MinConns 保持 0：Cloud Run 实例会被随时回收，预热连接没有意义，
	// 反而会在缩容时占着数据库的连接名额不放。
	cfg.MinConns = 0
	// 空闲连接活不过一次冷启动周期，短一点让数据库更快收回名额。
	cfg.MaxConnIdleTime = 2 * time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	// 连接建立超时留短：Cloud Run 请求本身有超时，连不上就快速失败比挂住好。
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("创建连接池失败: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库不可达: %w", err)
	}

	return &Store{Pool: pool, Queries: dbgen.New(pool)}, nil
}

// Close 释放连接池。
func (s *Store) Close() {
	if s.Pool != nil {
		s.Pool.Close()
	}
}

// InTx 在事务里跑 fn，出错自动回滚。
//
// 用于必须原子的场景：下单（写 orders + order_transitions）、
// 流量入账（累加 user_traffic + 写 stat_user_server）、佣金结算（双录记账）。
func (s *Store) InTx(ctx context.Context, fn func(*dbgen.Queries) error) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	// 已提交时 Rollback 返回 ErrTxClosed，忽略即可。
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}

// Health 供 /healthz 使用。只探连接，不查业务表 —— 健康检查不该被业务数据影响。
func (s *Store) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.Pool.Ping(ctx)
}
