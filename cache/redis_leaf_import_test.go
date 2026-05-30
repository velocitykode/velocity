package cache_test

// The cache root tests construct managers that resolve the "redis" store
// (lock, remember, and manager tests). Redis now lives in the cache/redis
// leaf and self-registers from its init(); blank-import it here so the cache
// test binary can resolve the driver, mirroring how an application enables
// redis in production.
import _ "github.com/velocitykode/velocity/cache/redis"
