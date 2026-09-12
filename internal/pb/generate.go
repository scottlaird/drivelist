// Package pb holds the generated protobuf and connect code for the
// drivelist.v1 API. Regenerate after editing proto/drivelist/v1/drivelist.proto
// with `go generate ./internal/pb`; it needs protoc, protoc-gen-go and
// protoc-gen-connect-go on PATH.
package pb

//go:generate protoc -I ../../proto -I /opt/homebrew/include -I /usr/include --go_out=../.. --go_opt=module=github.com/scottlaird/drivelist --connect-go_out=../.. --connect-go_opt=module=github.com/scottlaird/drivelist drivelist/v1/drivelist.proto
