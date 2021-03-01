package eredis

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

var (
	luaRefresh = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("pexpire", KEYS[1], ARGV[2]) else return 0 end`)
	luaRelease = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`)
	luaPTTL    = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("pttl", KEYS[1]) else return -3 end`)
)

// lockClient wraps a redis client.
type lockClient struct {
	client redis.Cmdable
	tmp    []byte
	tmpMu  sync.Mutex
}

// Obtain tries to obtain a new lock using a key with the given TTL.
// May return ErrNotObtained if not successful.
func (c *lockClient) Obtain(ctx context.Context, key string, ttl time.Duration, opts ...LockOption) (*lock, error) {
	// Create a random token
	token, err := c.randomToken()
	if err != nil {
		return nil, err
	}
	opt := &lockOption{}
	for _, o := range opts {
		o(opt)
	}
	if opt.retryStrategy != nil {
		opt.retryStrategy = NoRetry()
	}

	value := token + opt.metadata
	retry := opt.retryStrategy

	deadlinectx, cancel := context.WithDeadline(ctx, time.Now().Add(ttl))
	defer cancel()

	var timer *time.Timer
	for {
		ok, err := c.obtain(deadlinectx, key, value, ttl)
		if err != nil {
			return nil, err
		} else if ok {
			return &lock{client: c, key: key, value: value}, nil
		}

		backoff := retry.NextBackoff()
		if backoff < 1 {
			return nil, ErrNotObtained
		}

		if timer == nil {
			timer = time.NewTimer(backoff)
			defer timer.Stop()
		} else {
			timer.Reset(backoff)
		}

		select {
		case <-deadlinectx.Done():
			return nil, ErrNotObtained
		case <-timer.C:
		}
	}
}

func (c *lockClient) obtain(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	return c.client.SetNX(ctx, key, value, ttl).Result()
}

func (c *lockClient) randomToken() (string, error) {
	c.tmpMu.Lock()
	defer c.tmpMu.Unlock()

	if len(c.tmp) == 0 {
		c.tmp = make([]byte, 16)
	}

	if _, err := io.ReadFull(rand.Reader, c.tmp); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(c.tmp), nil
}

// lock represents an obtained, distributed lock.
type lock struct {
	client *lockClient
	key    string
	value  string
}

// Key returns the redis key used by the lock.
func (l *lock) Key() string {
	return l.key
}

// Token returns the token value set by the lock.
func (l *lock) Token() string {
	return l.value[:22]
}

// Metadata returns the metadata of the lock.
func (l *lock) Metadata() string {
	return l.value[22:]
}

// TTL returns the remaining time-to-live. Returns 0 if the lock has expired.
func (l *lock) TTL(ctx context.Context) (time.Duration, error) {
	res, err := luaPTTL.Run(ctx, l.client.client, []string{l.key}, l.value).Result()
	if err == redis.Nil {
		return 0, nil
	} else if err != nil {
		return 0, err
	}

	if num := res.(int64); num > 0 {
		return time.Duration(num) * time.Millisecond, nil
	}
	return 0, nil
}

// Refresh extends the lock with a new TTL.
// May return ErrNotObtained if refresh is unsuccessful.
func (l *lock) Refresh(ctx context.Context, ttl time.Duration, opts ...LockOption) error {
	ttlVal := strconv.FormatInt(int64(ttl/time.Millisecond), 10)
	status, err := luaRefresh.Run(ctx, l.client.client, []string{l.key}, l.value, ttlVal).Result()
	if err != nil {
		return err
	} else if status == int64(1) {
		return nil
	}
	return ErrNotObtained
}

// Release manually releases the lock.
// May return ErrLockNotHeld.
func (l *lock) Release(ctx context.Context) error {
	res, err := luaRelease.Run(ctx, l.client.client, []string{l.key}, l.value).Result()
	if err == redis.Nil {
		return ErrLockNotHeld
	} else if err != nil {
		return err
	}

	if i, ok := res.(int64); !ok || i != 1 {
		return ErrLockNotHeld
	}
	return nil
}

type LockOption func(c *lockOption)

// Options describe the options for the lock
type lockOption struct {
	// retryStrategy allows to customise the lock retry strategy.
	// Default: do not retry
	retryStrategy RetryStrategy

	// metadata string is appended to the lock token.
	metadata string
}

func (o *LockOption) WithMetadata(md string) LockOption {
	return func(lo *lockOption) {
		lo.metadata = md
	}
}

func (o *LockOption) WithRetryStrategy(retryStrategy RetryStrategy) LockOption {
	return func(lo *lockOption) {
		lo.retryStrategy = retryStrategy
	}
}
