package repository

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const imageTaskKeyPrefix = "image_task:"

var saveImageTaskScript = redis.NewScript(`
local previous = redis.call('GET', KEYS[1])
if previous then
  local task = cjson.decode(previous)
  if task.status ~= 'processing' then return 0 end
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return 1
`)

type imageTaskStore struct {
	rdb *redis.Client
}

func NewImageTaskStore(rdb *redis.Client) service.ImageTaskStore {
	return &imageTaskStore{rdb: rdb}
}

func (s *imageTaskStore) Save(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	ttl = min(ttl, service.MediaRetention)
	if task.CompletedAt != nil {
		deadline := time.Unix(*task.CompletedAt, 0).Add(service.MediaRetention)
		if task.ExpiresAt > 0 && time.Unix(task.ExpiresAt, 0).Before(deadline) {
			deadline = time.Unix(task.ExpiresAt, 0)
		}
		ttl = min(ttl, time.Until(deadline))
	}
	if ttl < time.Millisecond {
		return service.ErrImageTaskNotFound
	}
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	return saveImageTaskScript.Run(ctx, s.rdb, []string{imageTaskKey(task.ID)}, data, ttl.Milliseconds()).Err()
}

func (s *imageTaskStore) Get(ctx context.Context, id string) (*service.ImageTaskRecord, error) {
	data, err := s.rdb.Get(ctx, imageTaskKey(id)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, service.ErrImageTaskNotFound
		}
		return nil, err
	}
	var task service.ImageTaskRecord
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func imageTaskKey(id string) string {
	return imageTaskKeyPrefix + strings.TrimSpace(id)
}
