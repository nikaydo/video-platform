module github.com/nikaydo/video-platform

go 1.24.2

require (
	github.com/caarlos0/env/v11 v11.3.1
	github.com/go-chi/chi/v5 v5.2.2
	github.com/joho/godotenv v1.5.1
	github.com/nikaydo/grpc-contract v0.0.0
	google.golang.org/grpc v1.73.0
)

require (
	golang.org/x/net v0.38.0 // indirect
	golang.org/x/sys v0.31.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250324211829-b45e905df463 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
)

replace github.com/nikaydo/grpc-contract => ../grpc-contract
