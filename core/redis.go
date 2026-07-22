package core

import "github.com/redis/go-redis/v9"

// RedisService hands plugins a shared Redis client. It exists for plugins whose
// backend genuinely needs Redis semantics a key/value cache can't express —
// pipelines, lists (BLPOP), hashes, TTLs — the first being the usenet plugin's
// `staging: redis` assembly buffer (a verbatim lift of the production pipeline).
//
// Redis is OPTIONAL infrastructure. Unlike Storage/Auth/etc., a host may run
// with no Redis at all (the base site defaults every Redis-capable plugin to its
// durable Postgres mode), so `Core.Redis` is nil in that case and `New` does not
// require it. Consumers MUST nil-check `c.Redis` and degrade — never assume it.
//
// The client type is go-redis' UniversalClient (single/sentinel/cluster) so the
// seam doesn't pin a topology, and the raw client is exposed (not a narrowed
// interface) precisely so a plugin can lift battle-tested Redis code unchanged
// instead of routing every command through a lowest-common-denominator wrapper.
// The HOST owns the client's lifecycle: it builds the client from its own config
// and Close()s it on shutdown (same ownership model as the shared *sqlx.DB pool
// — Core exposes, the host owns).
type RedisService interface {
	Client() redis.UniversalClient
}

type redisService struct{ client redis.UniversalClient }

// NewRedis wraps a host-built client as a RedisService. Pass the result via
// Deps.Redis. A host without Redis simply omits Deps.Redis (leaving Core.Redis
// nil) rather than passing NewRedis(nil).
func NewRedis(client redis.UniversalClient) RedisService {
	return &redisService{client: client}
}

func (r *redisService) Client() redis.UniversalClient { return r.client }
