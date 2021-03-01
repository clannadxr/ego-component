package eredis

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gotomicro/ego/core/econf"
	"github.com/stretchr/testify/assert"
)

func TestLock(t *testing.T) {
	cmp := newCmpLock(t)
	l := cmp.NewLockClient()
	ctx := context.Background()
	lock, err := l.Obtain(ctx, "my-key", 100*time.Millisecond)
	if err == ErrNotObtained {
		t.Log("Could not obtain lock!")
	} else if err != nil {
		t.Fatal(err)
	}
	defer lock.Release(ctx)
	t.Log("I have a lock!")

	// Sleep and check the remaining TTL.
	time.Sleep(50 * time.Millisecond)
	if ttl, err := lock.TTL(ctx); err != nil {
		t.Fatal("check ttl fail,", err)
	} else if ttl > 0 {
		t.Log("Yay, I still have my lock!")
	}

	// Extend my lock.
	if err := lock.Refresh(ctx, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Sleep a little longer, then check.
	time.Sleep(100 * time.Millisecond)
	if ttl, err := lock.TTL(ctx); err != nil {
		t.Fatal(err)
	} else if ttl == 0 {
		t.Log("Now, my lock has expired!")
	}
}

func newCmpLock(t *testing.T) *Component {
	conf := `
[redis]
	debug=true
	addr="localhost:6379"
	enableAccessInterceptor = true
	enableAccessInterceptorReq = true
	enableAccessInterceptorRes = true
`
	err := econf.LoadFromReader(strings.NewReader(conf), toml.Unmarshal)
	assert.NoError(t, err)
	cmp := Load("redis").Build()
	return cmp
}
