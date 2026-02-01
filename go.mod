module github.com/velocitykode/velocity

go 1.25.1

retract (
	v1.0.0 // accidentally tagged
	v0.1.1 // accidentally tagged
)

require (
	github.com/alicebob/miniredis/v2 v2.36.1
	github.com/aws/aws-sdk-go v1.55.8
	github.com/brianvoe/gofakeit/v6 v6.28.0
	github.com/go-sql-driver/mysql v1.9.3
	github.com/golang-jwt/jwt/v5 v5.3.0
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/joho/godotenv v1.5.1
	github.com/lib/pq v1.10.9
	github.com/mattn/go-sqlite3 v1.14.32
	github.com/redis/go-redis/v9 v9.14.0
	golang.org/x/crypto v0.46.0
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	golang.org/x/time v0.14.0 // indirect
)
