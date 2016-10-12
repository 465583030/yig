package storage

import (
	"container/list"
	"git.letv.cn/yig/yig/helper"
	"git.letv.cn/yig/yig/redis"
	"github.com/mediocregopher/radix.v2/pubsub"
	"sync"
	"time"
)

// metadata is organized in 3 layers: YIG instance memory, Redis, HBase
type MetaCache struct {
	lock       *sync.RWMutex
	MaxEntries int
	lruList    *list.List
	// maps table -> key -> value
	cache                       map[redis.RedisDatabase]map[string]*list.Element
	failedCacheInvalidOperation chan entry
}

type entry struct {
	table redis.RedisDatabase
	key   string
	value interface{}
}

func newMetaCache() (m *MetaCache) {
	m.lock = new(sync.RWMutex)
	m.MaxEntries = helper.CONFIG.InMemoryCacheMaxEntryCount
	m.lruList = list.New()
	m.cache = make(map[redis.RedisDatabase]map[string]*list.Element)
	for _, table := range redis.MetadataTables {
		m.cache[table] = make(map[string]*list.Element)
	}
	m.failedCacheInvalidOperation = make(chan entry, helper.CONFIG.RedisConnectionNumber)
	go invalidLocalCache(m)
	go invalidRedisCache(m)
	return m
}

// subscribe to Redis channels and handle cache invalid info
func invalidLocalCache(m *MetaCache) {
	c, err := redis.GetClient()
	if err != nil {
		panic("Cannot get Redis client: " + err.Error())
	}

	subClient := pubsub.NewSubClient(c)
	subClient.PSubscribe(redis.InvalidQueueName + "*")
	for {
		response := subClient.Receive() // should block
		if response.Err != nil {
			if !response.Timeout() {
				helper.Logger.Println("Error receiving from redis channel:",
					response.Err)
			}
			continue
		}

		table, err := redis.TableFromChannelName(response.Channel)
		if err != nil {
			helper.Logger.Println("Bad redis channel name: ", response.Channel)
			continue
		}
		m.remove(table, response.Message)
	}
}

// redo failed invalid operation in MetaCache.failedCacheInvalidOperation channel
func invalidRedisCache(m *MetaCache) {
	for {
		failedEntry := <-m.failedCacheInvalidOperation
		err := redis.Invalid(failedEntry.table, failedEntry.key)
		if err != nil {
			m.failedCacheInvalidOperation <- failedEntry
			time.Sleep(1 * time.Second)
		}
	}
}

func (m *MetaCache) InvalidRedisCache(table redis.RedisDatabase, key string) {
	err := redis.Invalid(table, key)
	if err != nil {
		m.failedCacheInvalidOperation <- entry{
			table: table,
			key:   key,
		}
	}
}

func (m *MetaCache) Set(table redis.RedisDatabase, key string, value interface{}) {
	m.lock.Lock()
	if element, ok := m.cache[table][key]; ok {
		m.lruList.MoveToFront(element)
		element.Value.(*entry).value = value
		m.lock.Unlock()
		return
	}
	element := m.lruList.PushFront(&entry{table, key, value})
	m.cache[table][key] = element
	m.lock.Unlock()

	if m.lruList.Len() > m.MaxEntries {
		m.removeOldest()
	}

	m.InvalidRedisCache(table, key)
}

func (m *MetaCache) Get(table redis.RedisDatabase, key string,
	onCacheMiss func() (interface{}, error)) (value interface{}, err error) {

	m.lock.RLock()
	if element, hit := m.cache[table][key]; hit {
		m.lruList.MoveToFront(element)
		m.lock.RUnlock()
		return element.Value.(*entry).value, nil
	}
	m.lock.RUnlock()

	value, err = redis.Get(table, key)
	if err == nil && value != nil {
		return value, nil
	}

	if onCacheMiss != nil {
		value, err = onCacheMiss()
		if err != nil {
			return
		}

		// the returned error could be safely ignored,
		// only to cause another cache miss
		redis.Set(table, key, value)
		m.Set(table, key, value)
		return
	}
	return nil, nil
}

func (m *MetaCache) remove(table redis.RedisDatabase, key string) {
	m.lock.Lock()
	element, hit := m.cache[table][key]
	if hit {
		m.lruList.Remove(element)
		delete(m.cache[table], key)
	}
	m.lock.Unlock()
}

func (m *MetaCache) Remove(table redis.RedisDatabase, key string) {
	m.remove(table, key)
	m.InvalidRedisCache(table, key)
}

func (m *MetaCache) removeOldest() {
	m.lock.Lock()
	element := m.lruList.Back()
	if element != nil {
		toInvalid := element.Value.(*entry)
		m.lruList.Remove(element)
		delete(m.cache[toInvalid.table], toInvalid.key)
	}
	m.lock.Unlock()

	// Do not invalid Redis cache because data there is still _valid_
}

type DataCache struct {
}

func newDataCache() (d *DataCache) {
	return
}
