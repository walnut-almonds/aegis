module github.com/walnut-almonds/aegis/sdk/go

go 1.26.1

replace github.com/walnut-almonds/aegis => ../../

require (
	github.com/google/uuid v1.6.0
	github.com/walnut-almonds/aegis v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.80.0
)

require (
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260420184626-e10c466a9529 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
